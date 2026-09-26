// Runs before the page is painted, so styles that depend on scripting
// being available apply from the first frame and nothing shifts later.
document.documentElement.classList.replace("no-js", "js");
