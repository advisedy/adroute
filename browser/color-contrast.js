export async function textContrast(locator) {
  return locator.evaluate(el => {
    const rgb = value => value.match(/[\d.]+/g).map(Number);
    const luminance = color => color.slice(0, 3).map(c => {
      const v = c / 255;
      return v <= .04045 ? v / 12.92 : ((v + .055) / 1.055) ** 2.4;
    }).reduce((sum, v, i) => sum + v * [.2126, .7152, .0722][i], 0);
    let ancestor = el, background;
    while (ancestor) {
      const color = rgb(getComputedStyle(ancestor).backgroundColor);
      if (color.length === 3 || color[3] === 1) { background = color; break; }
      ancestor = ancestor.parentElement;
    }
    const text = luminance(rgb(getComputedStyle(el).color));
    const surface = luminance(background || [255, 255, 255]);
    return (Math.max(text, surface) + .05) / (Math.min(text, surface) + .05);
  });
}
