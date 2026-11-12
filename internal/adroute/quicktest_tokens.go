package adroute

import (
	"sync"

	"github.com/tiktoken-go/tokenizer/codec"
)

// One bundled reference vocabulary, not a claim about the upstream's tokenizer.
// No network downloads or model-specific tokenizer registry at runtime.
var quickTestTokenizer = sync.OnceValue(codec.NewCl100kBase)

func quickTestReferenceTokens(text string) *int {
	// This is a short fixed-prompt probe. Bound CPU work for malformed upstreams
	// rather than running BPE over an arbitrarily large response body.
	if len(text) > 4096 {
		return nil
	}
	count, err := quickTestTokenizer().Count(text)
	if err != nil || count < 32 {
		return nil
	}
	return &count
}
