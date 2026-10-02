package consoleserver

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const consoleRoleAssignmentLabel = "kelos.dev/console-role-assignment"

type consoleRole struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	CanAssign   bool   `json:"canAssign"`
}

var consoleRoles = []consoleRole{
	{Name: "user", Label: "User", Description: "Use Sessions and inspect Kelos resources"},
	{Name: "admin", Label: "Admin", Description: "Use Sessions, manage configuration, and assign Console roles"},
}

type consoleRoleAssignment struct {
	Binding         string `json:"binding"`
	ResourceVersion string `json:"resourceVersion"`
	Username        string `json:"username"`
	Role            string `json:"role"`
	Managed         bool   `json:"managed"`
	CanRemove       bool   `json:"canRemove"`
}

type consoleRoleInventory struct {
	Enabled        bool                    `json:"enabled"`
	UsernamePrefix string                  `json:"usernamePrefix,omitempty"`
	CurrentUser    string                  `json:"currentUser,omitempty"`
	Roles          []consoleRole           `json:"roles"`
	Assignments    []consoleRoleAssignment `json:"assignments"`
}

func rbacAccess(verb, resource, namespace, name string) authorizationv1.ResourceAttributes {
	return authorizationv1.ResourceAttributes{Group: rbacv1.GroupName, Verb: verb, Resource: resource, Namespace: namespace, Name: name}
}

func consoleRoleName(role string) string {
	for _, known := range consoleRoles {
		if role == known.Name {
			return "kelos-console-" + role
		}
	}
	return ""
}

func consoleRoleBindingName(username, role string) string {
	digest := sha256.Sum256([]byte(username + "\x00" + role))
	return fmt.Sprintf("kelos-console-access-%x", digest[:16])
}

func managedConsoleRoleBinding(binding *rbacv1.RoleBinding, role, username string) bool {
	return binding.Labels[consoleRoleAssignmentLabel] == "true" &&
		binding.Name == consoleRoleBindingName(username, role) &&
		binding.RoleRef == (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: consoleRoleName(role)}) &&
		len(binding.Subjects) == 1 && binding.Subjects[0] == (rbacv1.Subject{Kind: "User", APIGroup: rbacv1.GroupName, Name: username})
}

func (s *Server) adminRoles(writer http.ResponseWriter, request *http.Request, parts []string) {
	if s.oidc == nil {
		if len(parts) == 0 && request.Method == http.MethodGet {
			writeJSON(writer, http.StatusOK, consoleRoleInventory{Roles: []consoleRole{}, Assignments: []consoleRoleAssignment{}})
		} else {
			writeError(writer, http.StatusForbidden, "user role management requires OIDC authentication")
		}
		return
	}
	if len(parts) == 0 {
		switch request.Method {
		case http.MethodGet:
			s.listConsoleRoles(writer, request)
		case http.MethodPost:
			s.assignConsoleRole(writer, request)
		default:
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
		return
	}
	if len(parts) == 2 && request.Method == http.MethodDelete {
		s.removeConsoleRole(writer, request, parts[0], parts[1])
		return
	}
	writeError(writer, http.StatusNotFound, "not found")
}

func (s *Server) listConsoleRoles(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	if !s.requireAccess(writer, request, rbacAccess("list", "rolebindings", namespace, "")) {
		return
	}
	identity := request.Context().Value(principalKey{}).(principal)
	inventory := consoleRoleInventory{Enabled: true, UsernamePrefix: s.oidc.UsernamePrefix, CurrentUser: identity.username, Roles: []consoleRole{}, Assignments: []consoleRoleAssignment{}}
	canCreate, err := s.allowed(request, rbacAccess("create", "rolebindings", namespace, ""))
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
		return
	}
	for _, role := range consoleRoles {
		if canCreate {
			role.CanAssign, err = s.allowed(request, rbacAccess("bind", "clusterroles", namespace, consoleRoleName(role.Name)))
			if err != nil {
				writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
				return
			}
		}
		inventory.Roles = append(inventory.Roles, role)
	}
	var bindings rbacv1.RoleBindingList
	if err := s.client.List(request.Context(), &bindings, client.InNamespace(namespace)); err != nil {
		writeAdminError(writer, "RoleBindings in namespace", namespace, err)
		return
	}
	for _, binding := range bindings.Items {
		role := strings.TrimPrefix(binding.RoleRef.Name, "kelos-console-")
		roleName := consoleRoleName(role)
		if roleName == "" || binding.RoleRef.Name != roleName || binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.APIGroup != rbacv1.GroupName {
			continue
		}
		for _, subject := range binding.Subjects {
			if subject.Kind != "User" || subject.APIGroup != rbacv1.GroupName || !strings.HasPrefix(subject.Name, s.oidc.UsernamePrefix) {
				continue
			}
			assignment := consoleRoleAssignment{Binding: binding.Name, ResourceVersion: binding.ResourceVersion, Username: subject.Name, Role: role, Managed: managedConsoleRoleBinding(&binding, role, subject.Name)}
			if assignment.Managed {
				assignment.CanRemove = true
				for _, verb := range []string{"get", "delete"} {
					allowed, err := s.allowed(request, rbacAccess(verb, "rolebindings", namespace, binding.Name))
					if err != nil {
						writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
						return
					}
					assignment.CanRemove = assignment.CanRemove && allowed
				}
			}
			inventory.Assignments = append(inventory.Assignments, assignment)
		}
	}
	sort.Slice(inventory.Assignments, func(i, j int) bool {
		a, b := inventory.Assignments[i], inventory.Assignments[j]
		if a.Username != b.Username {
			return a.Username < b.Username
		}
		if a.Role != b.Role {
			return a.Role < b.Role
		}
		return a.Binding < b.Binding
	})
	writeJSON(writer, http.StatusOK, inventory)
}

func (s *Server) assignConsoleRole(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	if !s.requireAccess(writer, request, rbacAccess("create", "rolebindings", namespace, "")) {
		return
	}
	var payload struct {
		Subject string `json:"subject"`
		Role    string `json:"role"`
	}
	if err := decodeJSON(request.Body, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, err.Error())
		return
	}
	roleName := consoleRoleName(payload.Role)
	if roleName == "" || !validIdentityValue(payload.Subject, 1024) {
		writeError(writer, http.StatusBadRequest, "a valid OIDC subject and a User or Admin role are required")
		return
	}
	if !s.requireAccess(writer, request, rbacAccess("bind", "clusterroles", namespace, roleName)) {
		return
	}
	var role rbacv1.ClusterRole
	if err := s.client.Get(request.Context(), client.ObjectKey{Name: roleName}, &role); err != nil {
		writeAdminError(writer, "ClusterRole", roleName, err)
		return
	}
	username := s.oidc.UsernamePrefix + payload.Subject
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: consoleRoleBindingName(username, payload.Role), Namespace: namespace, Labels: map[string]string{consoleRoleAssignmentLabel: "true"}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: roleName},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: username}},
	}
	if err := s.client.Create(request.Context(), binding); err != nil {
		writeAdminError(writer, "role assignment for user", username, err)
		return
	}
	writeJSON(writer, http.StatusCreated, consoleRoleAssignment{Binding: binding.Name, ResourceVersion: binding.ResourceVersion, Username: username, Role: payload.Role, Managed: true})
}

func (s *Server) removeConsoleRole(writer http.ResponseWriter, request *http.Request, namespace, name string) {
	if !s.requireAccess(writer, request, rbacAccess("get", "rolebindings", namespace, name), rbacAccess("delete", "rolebindings", namespace, name)) {
		return
	}
	version := request.URL.Query().Get("resourceVersion")
	if version == "" {
		writeError(writer, http.StatusBadRequest, "resourceVersion is required to remove a role assignment")
		return
	}
	var binding rbacv1.RoleBinding
	if err := s.client.Get(request.Context(), client.ObjectKey{Namespace: namespace, Name: name}, &binding); err != nil {
		writeAdminError(writer, "RoleBinding", name, err)
		return
	}
	role := strings.TrimPrefix(binding.RoleRef.Name, "kelos-console-")
	if len(binding.Subjects) != 1 || consoleRoleName(role) == "" || !strings.HasPrefix(binding.Subjects[0].Name, s.oidc.UsernamePrefix) || !managedConsoleRoleBinding(&binding, role, binding.Subjects[0].Name) {
		writeError(writer, http.StatusForbidden, "only role assignments created by this Console can be removed here")
		return
	}
	if err := s.client.Delete(request.Context(), &binding, client.Preconditions{UID: &binding.UID, ResourceVersion: &version}); err != nil {
		writeAdminError(writer, "RoleBinding", name, err)
		return
	}
	writeJSON(writer, http.StatusOK, map[string]bool{"removed": true})
}
