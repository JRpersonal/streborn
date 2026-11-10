// live.js: a screen-reader status region that lives outside the repainted
// markup, shared by the UI 2.0 speaker list and the groups page.
//
// Each caller owns ONE region (its own id), created empty on first use and
// appended to <body>, so a repaint of the view never removes it and a screen
// reader has registered it before the first status is written into it. What
// is said, and when, stays with the caller: the list re-announces the same
// words, the groups page only a change.

// liveRegion returns the handle of one polite status region.
//   ensure()    the node, created and attached if it is not on the page;
//   attached()  whether the node is on the page right now, asked BEFORE
//               ensure() by a caller that must treat a region that only now
//               joined the page differently.
export function liveRegion(id) {
  let node = null;
  const attached = () => !!node && document.body.contains(node);
  const ensure = () => {
    if (attached()) return node;
    node = document.createElement('div');
    node.id = id;
    node.className = 'u-sr-only';
    node.setAttribute('role', 'status');
    node.setAttribute('aria-live', 'polite');
    document.body.appendChild(node);
    return node;
  };
  return { ensure, attached };
}
