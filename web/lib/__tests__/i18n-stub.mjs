// Korean source strings with {vars} filled in, like t() without a dictionary.
export const t = (s, vars = {}) => s.replace(/\{(\w+)\}/g, (m, k) => (k in vars ? String(vars[k]) : m));
export const dateLocale = () => "ko-KR";
export const getLocale = () => "ko";
