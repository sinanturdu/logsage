@AGENTS.md

## Claude Code specifics
- Plan before coding on any task touching more than 2 files; show the plan and wait for approval.
- Run `go vet ./... && go test ./...` before declaring a task finished.
- Update `docs/roadmap.md` checkboxes and the relevant docs in the same change as the code.
