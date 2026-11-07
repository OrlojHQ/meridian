import { normalizeAPIError } from "../api/client";
import { boundedThreadText } from "./threadChat";

export function ThreadError({ error }: { error: unknown }) {
  const normalized = normalizeAPIError(error);
  if (normalized.code === "transcript_locked" || normalized.status === 423) {
    return (
      <section className="locked-state" role="alert">
        <h2>Encrypted transcript locked</h2>
        <p>
          The installation key is missing or does not match this Thread. Ask an
          operator to restore the correct key, then retry. Ciphertext and key
          details are intentionally hidden.
        </p>
      </section>
    );
  }
  if (normalized.code === "transcript_corrupt") {
    return (
      <section className="error-state" role="alert">
        <h2>Encrypted transcript corrupt</h2>
        <p>
          Stop using this Thread and restore from a trusted backup. Ciphertext
          and key details are intentionally hidden.
        </p>
      </section>
    );
  }
  return (
    <section className="error-state" role="alert">
      <h2>Thread request failed</h2>
      <p>{boundedThreadText(normalized.message)}</p>
    </section>
  );
}
