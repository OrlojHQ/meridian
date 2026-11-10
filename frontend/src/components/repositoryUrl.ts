// Detects the GitHub forms localrepo.Normalize rewrites to HTTPS. The server
// stores the rewritten URL; the form uses this only to ask for a clone token.
export function isGitHubRepositoryInput(value: string): boolean {
  const trimmed = value.trim();
  if (trimmed === "" || /[\u0000\r\n\s]/.test(trimmed)) return false;
  if (/^git@github\.com:/i.test(trimmed)) return true;
  if (/^ssh:\/\/(?:git@)?github\.com\//i.test(trimmed)) return true;
  if (/^https:\/\/github\.com\//i.test(trimmed)) return true;
  if (/^github\.com\//i.test(trimmed)) return true;
  if (trimmed.includes("://") || trimmed.includes("@") || trimmed.includes("\\")) {
    return false;
  }
  const [owner, repository, extra] = trimmed.split("/");
  if (extra !== undefined || !owner || !repository) return false;
  if (owner === "." || owner === ".." || repository === "." || repository === "..") {
    return false;
  }
  return !/[ :@?#\\%]/.test(`${owner}${repository}`);
}
