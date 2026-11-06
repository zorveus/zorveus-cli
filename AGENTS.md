# CLI scope

Build against the existing Zorveus backend APIs and ID system. CLI implementation does not authorize backend source changes, database migrations, OAuth client-policy changes, or new backend endpoints. Make a backend change only when the user explicitly requests that specific change

If an existing endpoint needs another permission or scope, report the endpoint and required access to the user. The user owns those backend changes

Keep routing aliases in the manifest and CLI bookkeeping. Resolve them to the existing routing membership selectors and resource IDs. Do not add backend-owned aliases or routing-group IDs

Reuse the existing `/me` endpoint when session details are needed. Verify its actual authentication behavior instead of adding a parallel session endpoint

Write straightforward, skimmable code with clear names and early returns. Avoid cleverness and unnecessary abstractions

Never commit or push unless the user explicitly authorizes that specific action for the current work. Permission to commit does not imply permission to push, and permission to push does not imply permission to create or amend commits
