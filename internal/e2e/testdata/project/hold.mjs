import { spawn } from "node:child_process";
import { writeFileSync } from "node:fs";

const child = spawn(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
  stdio: "ignore",
});
writeFileSync("hold.json", JSON.stringify({
  parent: process.pid,
  child: child.pid,
  secretPresent: Object.hasOwn(process.env, "LOKI_E2E_SECRET"),
}));
console.log("ready");
setInterval(() => {}, 1000);
