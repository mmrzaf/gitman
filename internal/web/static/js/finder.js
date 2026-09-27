// The file finder: "Go to file" on a Files page opens a picker over every
// file at the current ref, filtered as you type. The paths come from
// GET /{repo}/tree-paths?ref=…, fetched once per ref for as long as the
// page is open; a failed fetch is not kept, so opening again retries.
import { on } from "./dom.js";
import { filesURL } from "./address.js";
import { Picker } from "./picker.js";

const cache = new Map();
let picker = null;

async function pathsFor(repo, ref) {
  const key = `${repo}@${ref}`;
  if (cache.has(key)) return cache.get(key);
  let response;
  try {
    response = await fetch(`/${encodeURIComponent(repo)}/tree-paths?ref=${encodeURIComponent(ref)}`, {
      headers: { Accept: "application/json" },
    });
  } catch {
    throw new Error("Couldn't reach Gitman to list the files. Check your connection and try again.");
  }
  if (response.redirected) throw new Error("Your session has ended. Reload the page to sign in again.");
  if (!response.ok) throw new Error("Couldn't list the files at this ref.");
  const paths = await response.json();
  cache.set(key, paths);
  return paths;
}

on("click", "[data-file-finder-open]", async (event, button) => {
  const { repo, ref } = button.dataset;
  picker ||= new Picker({ label: "Go to file", placeholder: "Find a file by name or path…" });
  picker.setMessage("Loading the file list…");
  picker.open();
  try {
    const paths = await pathsFor(repo, ref);
    picker.setItems(paths.map((path) => ({ label: path, href: filesURL(repo, ref, path), icon: "file" })));
  } catch (err) {
    picker.setMessage(err.message);
  }
});
