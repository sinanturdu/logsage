package api

import "github.com/sinanturdu/logsage/backend/internal/storage/postgres"

// action is something a project member may do.
type action int

const (
	actView         action = iota // read the project and its key list
	actManageKeys                 // create and revoke keys
	actDisable                    // disable the project
	actManageGroups               // change an error group's status (ADR 0022)
	actManageAlerts               // create and change alert rules and channels (ADR 0024)
	actManageIncidents            // create and change incidents (ADR 0026)
)

// can is the role matrix (ADR 0013). Non-members never reach it: they get
// 404 before any role check.
func can(role postgres.Role, a action) bool {
	switch a {
	case actView:
		return role == postgres.RoleOwner || role == postgres.RoleAdmin || role == postgres.RoleViewer
	case actManageKeys, actManageGroups, actManageAlerts, actManageIncidents:
		return role == postgres.RoleOwner || role == postgres.RoleAdmin
	case actDisable:
		return role == postgres.RoleOwner
	}
	return false
}
