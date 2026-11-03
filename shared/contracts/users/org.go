package users

// The org zone: companies, groups and invitations. As in the profile zone,
// the person is the one in the envelope.

// Subjects of the org zone.
const (
	SubjectOrgCompanyCreate = "rpc.users.org.company.create"
	SubjectOrgCompanyGet    = "rpc.users.org.company.get"
	SubjectOrgCompanyUpdate = "rpc.users.org.company.update"
	SubjectOrgCompanyLeave  = "rpc.users.org.company.leave"
	SubjectOrgCompanyInvite = "rpc.users.org.company.invite"
	SubjectOrgCompanyRemove = "rpc.users.org.company.remove"

	SubjectOrgGroupCreate = "rpc.users.org.group.create"
	SubjectOrgGroupGet    = "rpc.users.org.group.get"
	SubjectOrgGroupDelete = "rpc.users.org.group.delete"
	SubjectOrgGroupLeave  = "rpc.users.org.group.leave"
	SubjectOrgGroupInvite = "rpc.users.org.group.invite"
	SubjectOrgGroupRemove = "rpc.users.org.group.remove"

	SubjectOrgInviteList    = "rpc.users.org.invite.list"
	SubjectOrgInviteAccept  = "rpc.users.org.invite.accept"
	SubjectOrgInviteDecline = "rpc.users.org.invite.decline"

	SubjectOrgSnapshot = "rpc.users.org.snapshot" // where an order goes, for core
)

// Error codes of the org zone.
const (
	ErrNotOwner         = "not_owner"          // only the owner may do this
	ErrNotInCompany     = "not_in_company"     //
	ErrNotInGroup       = "not_in_group"       //
	ErrInCompany        = "already_in_company" // leave the old one first
	ErrInGroup          = "already_in_group"   // leave the old one first
	ErrOwnerStays       = "owner_stays"        // the owner cannot leave while others are in the company
	ErrUserNotFound     = "user_not_found"     // nobody with this nickname or phone
	ErrInviteExists     = "invite_exists"      // this person already has this invitation
	ErrInviteExpired    = "invite_expired"     //
	ErrOtherCompany     = "other_company"      // a company group takes only people of the company
	ErrCannotInviteSelf = "cannot_invite_self" //
)

// Member is one person of a company or group.
type Member struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
	Nickname    string `json:"nickname,omitempty"`
	JoinedMs    int64  `json:"joined_ms"`
	IsOwner     bool   `json:"is_owner"`
}

// CompanyCreateRequest makes a company; whoever asks becomes its owner.
type CompanyCreateRequest struct {
	Name string `json:"name"`
}

// CompanyUpdateRequest changes the fields that are present. MaxDeliveryFee
// is in bani; 0 = only free delivery. ClearFee removes the limit.
type CompanyUpdateRequest struct {
	Name           *string `json:"name,omitempty"`
	MaxDeliveryFee *int64  `json:"max_delivery_fee,omitempty"`
	ClearFee       bool    `json:"clear_fee,omitempty"`
}

// Company is the company of a person. Members come only for the owner.
type Company struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	IsOwner        bool     `json:"is_owner"`
	MaxDeliveryFee *int64   `json:"max_delivery_fee,omitempty"`
	MemberCount    int      `json:"member_count"`
	Members        []Member `json:"members,omitempty"`
}

// GroupCreateRequest makes a group. Free means "not of my company", for
// somebody who is in a company and wants a group outside it.
type GroupCreateRequest struct {
	Name    string   `json:"name"`
	Address string   `json:"address"`
	Lat     *float64 `json:"lat,omitempty"`
	Lon     *float64 `json:"lon,omitempty"`
	Notes   string   `json:"notes,omitempty"`
	Free    bool     `json:"free,omitempty"`
}

// Group is the group of a person.
type Group struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
	Notes       string   `json:"notes,omitempty"`
	CompanyID   string   `json:"company_id,omitempty"`
	CompanyName string   `json:"company_name,omitempty"`
	IsOwner     bool     `json:"is_owner"`
	Members     []Member `json:"members"`
}

// InviteRequest names who is invited: "@nickname" or a phone number.
type InviteRequest struct {
	Who string `json:"who"`
}

// RemoveRequest names a member to take out.
type RemoveRequest struct {
	UserID string `json:"user_id"`
}

// InviteIDRequest names an invitation.
type InviteIDRequest struct {
	ID string `json:"id"`
}

// Invite is one open invitation of a person.
type Invite struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"` // company | group
	Name      string `json:"name"` // of the company or group
	ExpiresMs int64  `json:"expires_ms"`
}

// InvitesResponse lists them.
type InvitesResponse struct {
	Items []Invite `json:"items"`
}

// SnapshotResponse is where an order of this person goes now: their group, its
// company and address. All empty = no group.
type SnapshotResponse struct {
	GroupID     string   `json:"group_id,omitempty"`
	GroupName   string   `json:"group_name,omitempty"`
	CompanyID   string   `json:"company_id,omitempty"`
	CompanyName string   `json:"company_name,omitempty"`
	Address     string   `json:"address,omitempty"`
	Lat         *float64 `json:"lat,omitempty"`
	Lon         *float64 `json:"lon,omitempty"`
}
