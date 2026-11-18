# ADR 0056: Thread attachments stay in the Capsule

- Status: Accepted
- Date: 2026-11-18
- Extends: ADR 0012 (encrypted transcripts), ADR 0017 (workspace access through capsuled), ADR 0030 (ACP adapter)

## Context

A Thread composer could send only text. Operators need to hand a file or an
image to the harness they are already talking to. The bytes are as sensitive
as the prompt: source, screenshots, and credentials. The transcript is
encrypted (ADR 0012). Workspace bytes already reach a Capsule only through
bounded `capsuled` RPCs (ADR 0017). An adapter frame is capped at 256 KiB, so
an image cannot ride inside the user-message frame.

ACP agents advertise `promptCapabilities.image` on `initialize`. Other
harnesses in this repo take a text prompt. A file the model can open from the
workspace is useful even when the model cannot take an image block.

## Decision

The composer accepts up to eight attachments, 4 MiB each and 8 MiB together.
Images are PNG, JPEG, GIF, and WebP, recognized by their bytes. Other files
must use an allowed extension. The Station rejects anything else with a
sentence the UI can show, and the UI hides image attach unless the latest
models event says the harness accepts images.

On send, the Station writes each file into the Capsule at
`.meridian/attachments/<id>/<n>-<name>` through a new `capsuled` write that
refuses every other path, follows no symlink, and stores a regular
non-executable file. The write is audited as `capsule.workspace_write` with
the Capsule id only: no name, no path, no bytes.

Attachments are the operator's files, not project changes, and the
workspace is the repository checkout. The same write keeps
`.meridian/attachments/.gitignore` containing `*`, so the tree does not show
in `git status`, does not make the Capsule look dirty, and is skipped by
`git add`. A deeper ignore file outranks the project's own rules. Delivery
staging also resets `.meridian/attachments` to HEAD, so a repository that
tracks a file there, or an edited ignore file, still cannot put an
attachment into a delivery commit.

The encrypted transcript stores the operator's text plus the attachment's
name, media type, size, workspace path, and kind. It does not store the
bytes. After a reload the thread shows a chip. A thumbnail is shown only
before send, from the browser's local file. Keeping the bytes in the
transcript would either blow the 1 MiB block limit or leave image plaintext
next to an encrypted transcript. The file remains in the Capsule.

The harness prompt is composed at delivery time. Files are named by their
absolute path under `/workspace`, because a profile's working directory may
be a subdirectory. The ACP adapter likewise resolves an image path from the
workspace root, through an `os.Root`, not from its own directory. Images are not pasted into that text. When the ACP agent
advertised image support, the adapter reads the file inside the Capsule and
adds an image content block to `session/prompt`. If it did not, the adapter
refuses the image and the Station has already rejected it. Claude, Codex, Pi,
and any harness that does not advertise image support still receive other
files as paths in the prompt.

## Consequences

A screenshot the operator attaches is on the Station only while the request
is handled, then only in the Capsule. A database copy does not contain the
image. Reloading the thread shows the file name, not the picture. An agent
that can read its workspace can open the path. An agent that cannot take
images is not offered the image control.

The write RPC is not a general workspace editor. Callers that need to change
other files still have no such RPC.
