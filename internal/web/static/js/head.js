// Runs before the page is painted, so styles that depend on scripting
// being available apply from the first frame and nothing shifts later.
document.documentElement.classList.replace("no-js", "js");

// The theme chosen in the account menu, if any, so the first frame is
// already in it. Without a choice the system's applies (see theme.js).
try {
  const theme = localStorage.getItem("gitman-theme");
  if (theme === "light" || theme === "dark") document.documentElement.dataset.theme = theme;
} catch { /* storage blocked: the system's theme applies */ }
