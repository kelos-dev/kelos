package consoleserver

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"net/http"
	"sort"
	"strings"

	authorizationv1 "k8s.io/api/authorization/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const consoleMembershipLabel = "kelos.dev/console-membership"

type consoleRole struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description"`
	CanAssign   bool   `json:"canAssign"`
}

var consoleRoles = []consoleRole{
	{Name: "user", Label: "User", Description: "Use Sessions and inspect resources"},
	{Name: "admin", Label: "Admin", Description: "Manage members and configuration"},
}

type memberSource struct {
	Binding string `json:"binding"`
	Role    string `json:"role"`
	Managed bool   `json:"managed"`
}

type consoleMember struct {
	ID        string         `json:"id"`
	Username  string         `json:"username"`
	Role      string         `json:"role"`
	Version   string         `json:"version"`
	Sources   []memberSource `json:"sources"`
	CanChange bool           `json:"canChange"`
	CanRemove bool           `json:"canRemove"`
	bindings  []*rbacv1.RoleBinding
}

type consoleGroupAccess struct {
	Name    string `json:"name"`
	Role    string `json:"role"`
	Binding string `json:"binding"`
}

type consoleMemberInventory struct {
	Enabled        bool                 `json:"enabled"`
	AuthMode       string               `json:"authMode,omitempty"`
	UsernamePrefix string               `json:"usernamePrefix,omitempty"`
	CurrentUser    string               `json:"currentUser,omitempty"`
	Roles          []consoleRole        `json:"roles"`
	Members        []consoleMember      `json:"members"`
	Groups         []consoleGroupAccess `json:"groups"`
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

func memberBindingName(username, role string) string {
	digest := sha256.Sum256([]byte(username + "\x00" + role))
	return fmt.Sprintf("kelos-console-member-%x", digest[:16])
}

func memberBinding(username, role, namespace string) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: memberBindingName(username, role), Namespace: namespace, Labels: map[string]string{consoleMembershipLabel: "true"}},
		RoleRef:    rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: consoleRoleName(role)},
		Subjects:   []rbacv1.Subject{{Kind: "User", APIGroup: rbacv1.GroupName, Name: username}},
	}
}

func managedMemberBinding(binding *rbacv1.RoleBinding, username string) bool {
	role := bindingConsoleRole(binding)
	expected := memberBinding(username, role, binding.Namespace)
	return role != "" && binding.Labels[consoleMembershipLabel] == "true" && binding.Name == expected.Name &&
		len(binding.Subjects) == 1 && binding.Subjects[0] == expected.Subjects[0]
}

func bindingConsoleRole(binding *rbacv1.RoleBinding) string {
	for _, role := range consoleRoles {
		if binding.RoleRef == (rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: consoleRoleName(role.Name)}) {
			return role.Name
		}
	}
	return ""
}

func collectMembers(bindings []rbacv1.RoleBinding, prefix, groupsPrefix string) ([]consoleMember, []consoleGroupAccess) {
	sort.Slice(bindings, func(i, j int) bool { return bindings[i].Name < bindings[j].Name })
	byUsername := map[string]*consoleMember{}
	groups := []consoleGroupAccess{}
	for i := range bindings {
		binding := &bindings[i]
		role := bindingConsoleRole(binding)
		if role == "" {
			continue
		}
		seen := map[string]bool{}
		for _, subject := range binding.Subjects {
			if subject.APIGroup != rbacv1.GroupName {
				continue
			}
			if subject.Kind == "Group" && strings.HasPrefix(subject.Name, groupsPrefix) {
				groups = append(groups, consoleGroupAccess{Name: subject.Name, Role: role, Binding: binding.Name})
			}
			if subject.Kind != "User" || !strings.HasPrefix(subject.Name, prefix) || seen[subject.Name] {
				continue
			}
			seen[subject.Name] = true
			member := byUsername[subject.Name]
			if member == nil {
				member = &consoleMember{ID: base64.RawURLEncoding.EncodeToString([]byte(subject.Name)), Username: subject.Name, Role: role}
				byUsername[subject.Name] = member
			}
			if role == "admin" {
				member.Role = role
			}
			member.Sources = append(member.Sources, memberSource{Binding: binding.Name, Role: role, Managed: managedMemberBinding(binding, subject.Name)})
			member.bindings = append(member.bindings, binding)
		}
	}
	members := make([]consoleMember, 0, len(byUsername))
	for _, member := range byUsername {
		digest := sha256.New()
		for _, binding := range member.bindings {
			fmt.Fprintf(digest, "%s\x00%s\x00%s\x00", binding.Name, binding.UID, binding.ResourceVersion)
		}
		member.Version = fmt.Sprintf("%x", digest.Sum(nil))
		members = append(members, *member)
	}
	sort.Slice(members, func(i, j int) bool { return members[i].Username < members[j].Username })
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Name != groups[j].Name {
			return groups[i].Name < groups[j].Name
		}
		return groups[i].Binding < groups[j].Binding
	})
	return members, groups
}

func (s *Server) adminMembers(writer http.ResponseWriter, request *http.Request, parts []string) {
	if s.authMode != AuthModeOIDC && s.authMode != AuthModeGitHub {
		if len(parts) == 0 && request.Method == http.MethodGet {
			writeJSON(writer, http.StatusOK, consoleMemberInventory{Roles: []consoleRole{}, Members: []consoleMember{}, Groups: []consoleGroupAccess{}})
		} else {
			writeError(writer, http.StatusForbidden, "member management requires OIDC or GitHub authentication")
		}
		return
	}
	if len(parts) == 0 && request.Method == http.MethodGet {
		s.listMembers(writer, request)
		return
	}
	if len(parts) == 0 && request.Method == http.MethodPost {
		s.saveMember(writer, request, s.requestNamespace(request), "")
		return
	}
	if len(parts) == 2 && (request.Method == http.MethodPut || request.Method == http.MethodDelete) {
		username, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || !strings.HasPrefix(string(username), s.proxyAuth.UsernamePrefix) || !validIdentityValue(strings.TrimPrefix(string(username), s.proxyAuth.UsernamePrefix), 1024) {
			writeError(writer, http.StatusBadRequest, "invalid member ID")
			return
		}
		s.saveMember(writer, request, parts[0], string(username))
		return
	}
	writeError(writer, http.StatusNotFound, "not found")
}

func (s *Server) readMembers(writer http.ResponseWriter, request *http.Request, namespace string) ([]consoleMember, []consoleGroupAccess, bool) {
	if !s.requireAccess(writer, request, rbacAccess("list", "rolebindings", namespace, "")) {
		return nil, nil, false
	}
	var bindings rbacv1.RoleBindingList
	if err := s.client.List(request.Context(), &bindings, client.InNamespace(namespace)); err != nil {
		writeAdminError(writer, "members in namespace", namespace, err)
		return nil, nil, false
	}
	members, groups := collectMembers(bindings.Items, s.proxyAuth.UsernamePrefix, s.proxyAuth.GroupsPrefix)
	return members, groups, true
}

func (s *Server) listMembers(writer http.ResponseWriter, request *http.Request) {
	namespace := s.requestNamespace(request)
	members, groups, ok := s.readMembers(writer, request, namespace)
	if !ok {
		return
	}
	identity := request.Context().Value(principalKey{}).(principal)
	inventory := consoleMemberInventory{Enabled: true, AuthMode: s.authMode, UsernamePrefix: s.proxyAuth.UsernamePrefix, CurrentUser: identity.username, Roles: []consoleRole{}, Members: members, Groups: groups}
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
	for i := range inventory.Members {
		member := &inventory.Members[i]
		hasManaged, hasExternal, canRemove := false, false, true
		for _, source := range member.Sources {
			if !source.Managed {
				hasExternal = true
				continue
			}
			hasManaged = true
			allowed, err := s.allowed(request, rbacAccess("delete", "rolebindings", namespace, source.Binding))
			if err != nil {
				writeError(writer, http.StatusServiceUnavailable, "authorization service unavailable")
				return
			}
			canRemove = canRemove && allowed
		}
		member.CanRemove = hasManaged && canRemove
		member.CanChange = member.CanRemove && !hasExternal && canCreate
	}
	writeJSON(writer, http.StatusOK, inventory)
}

func (s *Server) saveMember(writer http.ResponseWriter, request *http.Request, namespace, username string) {
	members, _, ok := s.readMembers(writer, request, namespace)
	if !ok {
		return
	}
	var payload struct {
		Subject string `json:"subject"`
		Role    string `json:"role"`
	}
	removing := request.Method == http.MethodDelete
	adding := request.Method == http.MethodPost
	if !removing {
		if err := decodeJSON(request.Body, &payload); err != nil {
			writeError(writer, http.StatusBadRequest, err.Error())
			return
		}
		if consoleRoleName(payload.Role) == "" || (adding && !validIdentityValue(payload.Subject, 1024)) || (!adding && payload.Subject != "") {
			writeError(writer, http.StatusBadRequest, "a valid user ID and a User or Admin role are required")
			return
		}
		if adding {
			username = s.proxyAuth.UsernamePrefix + payload.Subject
		}
	}
	var member *consoleMember
	for i := range members {
		if members[i].Username == username {
			member = &members[i]
			break
		}
	}
	if adding && member != nil {
		for _, source := range member.Sources {
			if !source.Managed {
				writeError(writer, http.StatusConflict, "this user already has access managed outside Console")
				return
			}
		}
		writeError(writer, http.StatusConflict, "member already exists; use Change role")
		return
	}
	if !adding {
		if member == nil {
			writeError(writer, http.StatusNotFound, "member not found")
			return
		}
		version := request.URL.Query().Get("version")
		if version == "" {
			writeError(writer, http.StatusBadRequest, "member version is required")
			return
		}
		if member.Version != version {
			writeError(writer, http.StatusConflict, "membership changed; refresh Members and try again")
			return
		}
	}
	var managed []*rbacv1.RoleBinding
	if member != nil {
		for _, binding := range member.bindings {
			if !managedMemberBinding(binding, username) {
				if !removing {
					writeError(writer, http.StatusForbidden, "this member's role is managed outside Console")
					return
				}
				continue
			}
			if !s.requireAccess(writer, request, rbacAccess("delete", "rolebindings", namespace, binding.Name)) {
				return
			}
			managed = append(managed, binding)
		}
		if len(managed) == 0 {
			writeError(writer, http.StatusForbidden, "this member is managed outside Console")
			return
		}
	}
	if !removing {
		roleName := consoleRoleName(payload.Role)
		if !s.requireAccess(writer, request, rbacAccess("create", "rolebindings", namespace, ""), rbacAccess("bind", "clusterroles", namespace, roleName)) {
			return
		}
		var role rbacv1.ClusterRole
		if err := s.client.Get(request.Context(), client.ObjectKey{Name: roleName}, &role); err != nil {
			writeAdminError(writer, "ClusterRole", roleName, err)
			return
		}
		hasRole := false
		for _, binding := range managed {
			hasRole = hasRole || bindingConsoleRole(binding) == payload.Role
		}
		// RoleBinding roleRef is immutable. Grant the requested role before removing other direct grants so a failed grant preserves access.
		if !hasRole {
			if err := s.client.Create(request.Context(), memberBinding(username, payload.Role, namespace)); err != nil {
				writeAdminError(writer, "saving member", username, err)
				return
			}
		}
	}
	for _, binding := range managed {
		if !removing && bindingConsoleRole(binding) == payload.Role {
			continue
		}
		if err := s.client.Delete(request.Context(), binding, client.Preconditions{UID: &binding.UID, ResourceVersion: &binding.ResourceVersion}); err != nil {
			writeAdminError(writer, "finishing membership change; refresh Members for", username, err)
			return
		}
	}
	status := http.StatusOK
	if adding {
		status = http.StatusCreated
	}
	writeJSON(writer, status, map[string]string{"username": username, "role": payload.Role})
}
