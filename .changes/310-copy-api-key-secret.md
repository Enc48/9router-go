### Dashboard: copying an API key no longer copies the mask

Issue #310 reported that the Copy button on an API-key row put the masked
display value on the clipboard (`sk-c07…c93e`) rather than the credential, so
the pasted key could not authenticate. Revealing the key first worked around it,
but copying a secret that depends on whether it happened to be on screen is not
a usable workflow.

The copy button took whatever the cell was rendering, and a cell that has not
been revealed renders `keyDisplay` — the mask. It now copies the stored secret
directly and ignores the reveal state, so Copy works on a freshly loaded
dashboard. A row the server holds no plaintext for (a caller authenticated with
an engine client key, or a row created before #199 whose secret was hashed away)
has nothing usable to copy; it now reports that instead of silently putting an
unusable mask on the clipboard.

Verified by a new end-to-end case that copies from an un-revealed row, reads the
clipboard, and authenticates the copied value against the running gateway with
`GET /v1/models` — it returns `sk-1…fe7b` before the fix and a working `sk-…`
key after.
