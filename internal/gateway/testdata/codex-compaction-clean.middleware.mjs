// Destination-scoped sanitizer. Never edits the caller's history in place.
// Requires a trusted `kind: "compact"` supplied by the Magpie host, not the body.
const own = (value, key) => Object.prototype.hasOwnProperty.call(value, key);
const object = value => value !== null && typeof value === 'object' && !Array.isArray(value);

function stripField(value) {
  if (!object(value) || !own(value, 'extra_content')) return value;
  const copy = { ...value };
  delete copy.extra_content;
  return copy;
}

export function onProvider(body, ctx) {
  // Missing host capabilities are a no-op. Never infer the destination from
  // the current conversation model, a user prompt or a caller-supplied flag.
  if (ctx?.protocol !== 'responses' || ctx?.kind !== 'compact'
    || ctx?.provider?.account !== 'codex' || !object(body) || !Array.isArray(body.input)) return;
  let changed = false;
  const input = body.input.map(item => {
    let copy = stripField(item);
    if (object(copy) && Array.isArray(copy.content)) {
      let contentChanged = false;
      const content = copy.content.map(part => {
        const clean = stripField(part);
        contentChanged ||= clean !== part;
        return clean;
      });
      if (contentChanged) copy = { ...copy, content };
    }
    changed ||= copy !== item;
    return copy;
  });
  return changed ? { ...body, input } : undefined;
}
