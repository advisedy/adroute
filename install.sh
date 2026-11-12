#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: ./install.sh [--binary FILE] [--prefix DIR] [--shell bash|zsh]
                    [--configure-path | --no-modify-path] [--help]
Builds the source into PREFIX/bin/adroute, or installs an explicit --binary.
EOF
}
fail() { printf 'install.sh: %s\n' "$*" >&2; exit 1; }

binary= prefix= shell_override= configure=0 no_modify=0
while (($#)); do
  case "$1" in
    --binary|--prefix|--shell)
      (($# >= 2)) || fail "$1 requires a value"
      key=$1 val=$2; shift 2
      case "$key" in
        --binary) binary=$val ;;
        --prefix) prefix=$val ;;
        --shell) shell_override=$val ;;
      esac
      ;;
    --configure-path) configure=1; shift ;;
    --no-modify-path) no_modify=1; shift ;;
    --help) usage; exit 0 ;;
    --*) fail "unknown option: $1" ;;
    *) fail "unexpected argument: $1" ;;
  esac
done
(( configure + no_modify <= 1 )) || fail '--configure-path conflicts with --no-modify-path'
[[ -z $shell_override || $shell_override == bash || $shell_override == zsh ]] || fail '--shell must be bash or zsh'
[[ -n ${HOME:-} && $HOME == /* ]] || fail 'HOME must be an absolute path'
prefix=${prefix:-"$HOME/.local"}
[[ $prefix == /* ]] || prefix="$PWD/$prefix"
[[ $prefix != *:* && $prefix != *$'\n'* && $prefix != *$'\r'* && $HOME != *:* && $HOME != *$'\n'* && $HOME != *$'\r'* ]] || fail 'PATH directories cannot contain colon or newline'
dest="$prefix/bin/adroute"
bindir=$(dirname "$dest")

declare -a staged=() target_staged=() snapshot_temps=() original_files=() backup_paths=() target_files=() target_modes=() target_ids=() target_changed=()
tmp= builddir= committed=0
cleanup() {
  local p
  for p in "${staged[@]}" "${snapshot_temps[@]}"; do [[ -z $p ]] || rm -f -- "$p"; done
  [[ -z $tmp ]] || rm -f -- "$tmp"
  [[ -z $builddir ]] || rm -rf -- "$builddir"
}
trap cleanup EXIT

check_ancestors() {
  local p=$1 cur=/ part
  local -a parts
  [[ $p == /* ]] || fail "path must be absolute: $p"
  IFS=/ read -r -a parts <<< "${p#/}"
  for part in "${parts[@]}"; do
    [[ -z $part ]] && continue
    cur=${cur%/}/$part
    [[ ! -L $cur ]] || fail "refusing symlink path ancestor: $cur"
  done
}
check_ancestors "$bindir"
mkdir -p -- "$bindir" || fail "cannot create install directory: $bindir"
check_ancestors "$bindir"
[[ ! -d $dest ]] || fail "destination is a directory: $dest"

tmp=$(mktemp "$bindir/.adroute-install.XXXXXX") || fail 'cannot stage binary'
if [[ -n $binary ]]; then
  [[ -f $binary && -x $binary ]] || fail "--binary must name an executable regular file: $binary"
  cp -- "$binary" "$tmp" || fail 'cannot copy supplied binary'
else
  command -v go >/dev/null 2>&1 || fail 'Go is required to build from source; use --binary FILE for a prebuilt executable'
  root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
  [[ -f $root/go.mod ]] || fail "source module not found at $root"
  builddir=$(mktemp -d "${TMPDIR:-/tmp}/adroute-build.XXXXXX") || fail 'cannot create build directory'
  (cd "$root" && go build -o "$builddir/adroute" ./cmd/adroute) || fail 'Go build failed; existing installation was left unchanged'
  cp -- "$builddir/adroute" "$tmp" || fail 'cannot stage built binary'
fi
chmod 0755 "$tmp"
mv -fT -- "$tmp" "$dest" || fail "cannot atomically install $dest"
tmp=
printf 'Installed executable: %s\n' "$dest"

shell=${shell_override:-${SHELL##*/}}
[[ $shell == bash || $shell == zsh ]] || shell=unknown
files=()
if [[ $shell == bash ]]; then
  files+=("$HOME/.bashrc")
  login=
  for f in "$HOME/.bash_profile" "$HOME/.bash_login" "$HOME/.profile"; do
    if [[ -e $f || -L $f ]]; then login=$f; break; fi
  done
  files+=("${login:-$HOME/.profile}")
elif [[ $shell == zsh ]]; then
  if [[ -n ${ZDOTDIR:-} ]]; then
    [[ $ZDOTDIR == /* ]] || fail 'ZDOTDIR is set but is not absolute; refusing to guess its effective directory'
    zd=$ZDOTDIR
  else
    zd=$HOME
  fi
  files+=("$zd/.zshrc")
fi

report_environment() {
  local current=no resolved
  case ":${PATH:-}:" in *":$bindir:"*) current=yes;; esac
  resolved=$(command -v adroute 2>/dev/null || true)
  if [[ -n $resolved && $resolved != "$dest" ]]; then
    printf 'Warning: current adroute resolves to %s; it may shadow the new executable.\n' "$resolved"
  elif [[ -z $resolved ]]; then
    printf 'No adroute command currently resolves in this process PATH.\n'
  fi
  printf 'Current process PATH %s %s; a child installer cannot change the parent PATH or command cache.\n' \
    "$([[ $current == yes ]] && printf 'includes' || printf 'does not include')" "$bindir"
  printf 'Use the installed executable directly as: %q\n' "$dest"
}
print_targets() {
  local f
  for f in "${files[@]}"; do
    if [[ -e $f || -L $f ]]; then printf 'PATH target (existing; will update only the managed block if needed): %s\n' "$f"
    else printf 'PATH target (will be created if consented): %s\n' "$f"; fi
  done
}

if [[ $shell == unknown ]]; then
  printf 'Shell not recognized; No startup file was selected or edited. Use --shell bash|zsh or configure PATH manually.\n'
  report_environment
  (( configure == 0 )) || fail 'PATH configuration unavailable for unknown shell'
  exit 0
fi
print_targets
if (( no_modify )); then
  printf 'PATH was not configured (--no-modify-path). A new terminal will not inherit a PATH change from this process.\n'
  report_environment
  exit 0
fi
if (( configure == 0 )); then
  if [[ ! -t 0 ]]; then
    printf 'PATH was NOT configured (noninteractive). Rerun with --configure-path to explicitly consent.\n'
    report_environment
    exit 0
  fi
  printf 'Will add a guarded PATH block to each target listed above; existing content outside the block is preserved.\n'
  printf 'Persist this PATH change? [y/N] '
  IFS= read -r answer || answer=
  if [[ $answer != y && $answer != Y && $answer != yes && $answer != YES ]]; then
    printf 'PATH was NOT configured. Startup files were not changed.\n'
    report_environment
    exit 0
  fi
fi

quote_shell() { local s=${1//\'/\'\\\'\'}; printf "'%s'" "$s"; }
block_for() {
  local path=$1 newline=$'\n'
  [[ ${2:-LF} == CRLF ]] && newline=$'\r\n'
  printf '# >>> ADROUTE PATH BEGIN >>>%s_adroute_install_bin=%s%s' "$newline" "$(quote_shell "$path")" "$newline"
  printf 'case ":$PATH:" in *":${_adroute_install_bin}:"*) ;; *) export PATH="${_adroute_install_bin}${PATH:+:$PATH}";; esac%s' "$newline"
  printf 'unset _adroute_install_bin%s# <<< ADROUTE PATH END <<<%s' "$newline" "$newline"
}
byte_at() {
  local file=$1 offset=$2
  dd if="$file" bs=1 skip="$offset" count=1 2>/dev/null | od -An -tu1 | tr -d ' \n'
}
marker_offsets() {
  local file=$1 marker=$2 label=$3 entry offset prev next after length=${#2}
  local -n result=$4
  result=()
  while IFS= read -r entry; do
    [[ -n $entry ]] || continue
    offset=${entry%%:*}
    if (( offset > 0 )); then
      prev=$(byte_at "$file" "$((offset - 1))")
      [[ $prev == 10 ]] || fail "malformed inline $label marker in $file (preceding byte=$prev)"
    fi
    next=$(byte_at "$file" "$((offset + length))")
    if [[ $next == 13 ]]; then
      after=$(byte_at "$file" "$((offset + length + 1))")
      [[ -z $after || $after == 10 ]] || fail "malformed inline $label marker in $file (following byte=$after)"
    else
      [[ -z $next || $next == 10 ]] || fail "malformed inline $label marker in $file (following byte=$next)"
    fi
    result+=("$offset")
  done < <(LC_ALL=C grep -aboF -- "$marker" "$file" || true)
}

for f in "${files[@]}"; do
  parent=$(dirname "$f")
  check_ancestors "$parent"
  [[ -d $parent ]] || fail "startup directory does not exist: $parent"
  if [[ -L $f || ( -e $f && ! -f $f ) ]]; then fail "refusing symlink or non-regular startup file: $f"; fi
  if [[ -f $f ]]; then
    links=$(stat -c %h -- "$f")
    [[ $links == 1 ]] || fail "refusing hard-linked startup file: $f"
    mode=$(stat -c %a -- "$f")
    identity=$(stat -c '%d:%i:%s:%y:%z:%a:%h' -- "$f")
    original=$(mktemp "$parent/.adroute-original.XXXXXX") || fail "cannot snapshot startup file: $f"
    snapshot_temps+=("$original")
    chmod 0600 "$original"
    cat -- "$f" > "$original" || fail "cannot read startup file: $f"
    [[ $(stat -c '%d:%i:%s:%y:%z:%a:%h' -- "$f") == "$identity" ]] || fail "startup file changed while being read: $f"
    cmp -s -- "$f" "$original" || fail "startup file contents changed while being read: $f"
  else
    mode=600 identity=absent original=
  fi
  target_files+=("$f") target_modes+=("$mode") target_ids+=("$identity")
  original_files+=("$original") backup_paths+=("")
done

verify_target() {
  local index=$1 context=$2 file=${target_files[$1]} expected=${target_ids[$1]}
  if [[ $expected == absent ]]; then
    [[ ! -e $file && ! -L $file ]] || fail "$context: startup file appeared: $file"
  else
    [[ -f $file && ! -L $file ]] || fail "$context: startup file identity changed: $file"
    [[ $(stat -c '%d:%i:%s:%y:%z:%a:%h' -- "$file") == "$expected" ]] || fail "$context: startup file identity changed: $file"
    cmp -s -- "$file" "${original_files[$index]}" || fail "$context: startup file contents changed: $file"
  fi
}

# Render only from the immutable snapshots captured above.
for i in "${!target_files[@]}"; do
  f=${target_files[$i]}; parent=$(dirname "$f"); source_file=${original_files[$i]}; mode=${target_modes[$i]}
  [[ ${target_ids[$i]} != absent ]] || { source_file=; mode=600; }
  staged_file=$(mktemp "$parent/.adroute-rc.XXXXXX") || fail "cannot stage $f"
  staged+=("$staged_file"); target_staged+=("$staged_file")
  start_offsets=() end_offsets=()
  if [[ -n $source_file ]]; then
    marker_offsets "$source_file" '# >>> ADROUTE PATH BEGIN >>>' start start_offsets
    marker_offsets "$source_file" '# <<< ADROUTE PATH END <<<' end end_offsets
  fi
  (( ${#start_offsets[@]} == ${#end_offsets[@]} )) || fail "unpaired AdRoute PATH markers in $f"
  (( ${#start_offsets[@]} <= 1 )) || fail "multiple AdRoute PATH marker pairs in $f"
  if (( ${#start_offsets[@]} == 1 )); then
    start=${start_offsets[0]} end=${end_offsets[0]}
    (( start < end )) || fail "reversed AdRoute PATH markers in $f"
    start_marker='# >>> ADROUTE PATH BEGIN >>>'; end_marker='# <<< ADROUTE PATH END <<<'
    start_next=$(byte_at "$source_file" "$((start + ${#start_marker}))")
    newline=LF; [[ $start_next == 13 ]] && newline=CRLF
    end_after=$((end + ${#end_marker})); end_next=$(byte_at "$source_file" "$end_after")
    if [[ $end_next == 13 ]]; then
      [[ $(byte_at "$source_file" "$((end_after + 1))") == 10 ]] || fail "malformed end marker line in $f"
      end_after=$((end_after + 2))
    elif [[ $end_next == 10 ]]; then end_after=$((end_after + 1)); fi
    block_file=$(mktemp "$parent/.adroute-block.XXXXXX") || fail "cannot stage block for $f"
    staged+=("$block_file"); block_for "$bindir" "$newline" > "$block_file"
    { head -c "$start" -- "$source_file"; cat -- "$block_file"; tail -c +"$((end_after + 1))" -- "$source_file"; } > "$staged_file"
  else
    [[ -z $source_file ]] || cat -- "$source_file" > "$staged_file"
    if [[ -s $staged_file ]]; then
      last=$(tail -c 1 -- "$staged_file" | od -An -tu1 | tr -d ' \n')
      [[ $last == 10 ]] || printf '\n' >> "$staged_file"
    fi
    block_path=$(mktemp "$parent/.adroute-block.XXXXXX") || fail "cannot stage block for $f"
    staged+=("$block_path"); block_for "$bindir" > "$block_path"
    cat -- "$block_path" >> "$staged_file"
  fi
  chmod "$mode" "$staged_file"
  if [[ -n $source_file ]] && cmp -s -- "$source_file" "$staged_file"; then
    rm -f -- "$staged_file"
    target_changed+=(no)
  else target_changed+=(yes); fi
done

# Detect edits made during staging before creating backups or committing any rc.
for i in "${!target_files[@]}"; do verify_target "$i" 'startup file changed during PATH preparation'; done

# Backups copy the captured original bytes, never a later reread of the live file.
for i in "${!target_files[@]}"; do
  [[ ${target_changed[$i]} == yes && ${target_ids[$i]} != absent ]] || continue
  f=${target_files[$i]}; backup=$(mktemp "$(dirname "$f")/.adroute-backup.XXXXXX") || fail "cannot create private backup for $f"
  snapshot_temps+=("$backup"); chmod 0600 "$backup"
  cat -- "${original_files[$i]}" > "$backup" || fail "cannot snapshot original $f"
  cmp -s -- "${original_files[$i]}" "$backup" || fail "backup verification failed for $f"
  backup_paths[$i]=$backup; snapshot_temps[-1]=
  printf 'Private unique backup created (0600): %s\n' "$backup"
done

# Verify all targets, including no-op files, before the first commit.
for i in "${!target_files[@]}"; do verify_target "$i" 'startup file changed before commit'; done

for i in "${!target_files[@]}"; do
  [[ ${target_changed[$i]} == yes ]] || continue
  f=${target_files[$i]}
  verify_target "$i" "partial PATH configuration after $committed committed file(s)"
  if ! mv -fT -- "${target_staged[$i]}" "$f"; then
    fail "partial PATH configuration after $committed committed file(s); backup(s) retained"
  fi
  target_staged[$i]=; committed=$((committed + 1))
  printf 'Updated startup file: %s\n' "$f"
done

if (( committed == 0 )); then printf 'PATH already configured by matching AdRoute blocks; no startup file or backup changed.\n'; fi
printf 'Persistent PATH configuration is in place for new %s shells. Open a new terminal; current parent PATH/cache remain unchanged.\n' "$shell"
report_environment
