### Fixed

- A client that hangs up is recorded as 499 in the request log again, not as a
  502. #455 removed the string matching that used to classify an abort and
  replaced it with `ue.StatusCode == StatusClientClosedRequest`, but the
  request-log call site in `usage.go` passes a **nil** context, so the
  `ctx.Err()` branch cannot fire there — and the forward error for a real
  disconnect is the raw transport error (a `*url.Error` wrapping
  `context.Canceled`), never an `*upstreamError`. The classifier now keeps the
  typed `errors.Is(err, context.Canceled)` check, which sees through `%w`
  wrapping and so restores the 499 without reintroducing the string matching
  #455 set out to remove. `context.DeadlineExceeded` and an error whose text
  merely says "client closed" are still upstream failures.
