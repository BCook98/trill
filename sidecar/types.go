package sidecar

type Session struct {
	Provider      string        `json:"provider,omitempty"`
	ProviderName  string        `json:"providerName,omitempty"`
	Capabilities  *Capabilities `json:"capabilities,omitempty"`
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	ProjectID     string        `json:"projectId"`
	ProjectName   string        `json:"projectName"`
	Status        string        `json:"status"`
	ActivityState string        `json:"activityState"`
	Agent         string        `json:"agent"`
	Branch        string        `json:"branch"`
	UpdatedAt     string        `json:"updatedAt"`
	Attachable    bool          `json:"attachable"`
	Resumable     bool          `json:"resumable"`
	Archived      bool          `json:"archived"`
}

type Project struct {
	ParentID       string        `json:"parentId,omitempty"`
	Branch         string        `json:"branch,omitempty"`
	IsWorktree     bool          `json:"isWorktree,omitempty"`
	Focused        bool          `json:"focused,omitempty"`
	WorkspaceOrder int           `json:"workspaceOrder,omitempty"`
	HerdrWorkspace string        `json:"-"`
	Provider       string        `json:"provider,omitempty"`
	ProviderName   string        `json:"providerName,omitempty"`
	Capabilities   *Capabilities `json:"capabilities,omitempty"`
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Path           string        `json:"path"`
}

type rawSession struct {
	ProviderKey       string `json:"-"`
	HerdrPane         string `json:"-"`
	HerdrTerminal     string `json:"-"`
	ID                string `json:"id"`
	Name              string `json:"name"`
	ProjectID         string `json:"projectId"`
	Status            string `json:"status"`
	ActivityState     string `json:"activityState"`
	CurrentAgent      string `json:"currentAgent"`
	Branch            string `json:"branch"`
	UpdatedAt         string `json:"updatedAt"`
	Mode              string `json:"mode"`
	Goal              string `json:"goal"`
	TerminalLive      bool   `json:"terminalLive"`
	HiddenFromSidebar bool   `json:"hiddenFromSidebar"`
	WorktreePath      string `json:"worktreePath"`
	Source            string `json:"source"`
}

type CreateOptions struct {
	Kind        string
	UseWorktree bool
	Branch      string
}
