// Ambient types for the model-picker test, which reads features.css from disk.
// The app tsconfig does not include Node. Do not import node:fs from browser code.
declare module "node:fs" {
  export function readFileSync(path: URL | string, encoding: "utf8"): string;
}
