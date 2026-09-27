// Fuzzy matching for pickers: every character of the query must appear
// in the text, in order, not necessarily together — "chgo" finds
// "internal/pay/charge.go".

// match returns { score, at } — lower scores are better matches, and at
// lists the matched characters' positions — or null when text does not
// match. A match that starts earlier, is tighter, or starts a word or a
// path segment scores better, so "app" puts "app.py" before
// "a-longer-path/app.py".
export function match(query, text) {
  const q = query.toLowerCase();
  if (q === "") return { score: text.length, at: [] };
  const t = text.toLowerCase();
  // Prefer a match whose last path segment holds the query, then any.
  const start = Math.max(t.lastIndexOf("/") + 1, 0);
  return scan(q, t, start) ?? (start > 0 ? scan(q, t, 0) : null);
}

function scan(q, t, from) {
  const at = [];
  let qi = 0;
  for (let i = from; i < t.length && qi < q.length; i++) {
    if (t[i] === q[qi]) {
      at.push(i);
      qi++;
    }
  }
  if (qi < q.length) return null;
  const span = at[at.length - 1] - at[0];
  const boundary = at[0] === 0 || "/-_. ".includes(t[at[0] - 1]) ? 0 : 5;
  const outside = from === 0 && t.includes("/") ? 20 : 0;
  return { score: span + at[0] * 0.1 + boundary + outside + t.length * 0.01, at };
}

// highlight is text as nodes, with the characters at the given positions
// in <mark>, so the reader sees why it matched.
export function highlight(text, at) {
  const nodes = [];
  const marked = new Set(at);
  let run = "";
  let inMark = false;
  const flush = () => {
    if (!run) return;
    if (inMark) {
      const mark = document.createElement("mark");
      mark.textContent = run;
      nodes.push(mark);
    } else {
      nodes.push(document.createTextNode(run));
    }
    run = "";
  };
  for (let i = 0; i < text.length; i++) {
    const m = marked.has(i);
    if (m !== inMark) {
      flush();
      inMark = m;
    }
    run += text[i];
  }
  flush();
  return nodes;
}
