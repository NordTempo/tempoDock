// Open /tests/keys.html under Vite. No backend: pure functions only.
import { isModEnter } from "./keys";

function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}

const press = (init: KeyboardEventInit, taken = false) => {
  const event = new KeyboardEvent("keydown", { cancelable: true, ...init });
  if (taken) event.preventDefault(); // as CodeMirror does for its own ⌘↵
  return event;
};

check(isModEnter(press({ key: "Enter", metaKey: true })), "⌘↵ applies");
check(isModEnter(press({ key: "Enter", ctrlKey: true })), "Ctrl+Enter applies");
check(!isModEnter(press({ key: "Enter" })), "a plain Enter doesn't apply");
check(
  !isModEnter(press({ key: "Enter", metaKey: true }, true)),
  "⌘↵ the SQL editor ran a query with doesn't also apply the staged changes",
);

document.title = "PASS: keys";
const results = document.getElementById("results");
if (results) results.textContent = "PASS: ⌘↵ applies only when nothing took it";
console.log(document.title);
