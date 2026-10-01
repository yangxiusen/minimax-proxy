package monitor

type TK2SDCounts struct {
	Running   int `json:"running"`
	Queued    int `json:"queued"`
	Succeeded int `json:"succeeded"`
	Uncertain int `json:"uncertain"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
}

type TK2SDAccount struct {
	Label   string `json:"label"`
	Status  string `json:"status"`
	Credits *int   `json:"credits"`
}

type TK2SDLogin struct {
	Status      string `json:"status"`
	CanDispatch bool   `json:"can_dispatch"`
}

type TK2SDDashboard struct {
	Counts        TK2SDCounts    `json:"counts"`
	Occupied      int            `json:"occupied"`
	Capacity      int            `json:"capacity"`
	Accounts      []TK2SDAccount `json:"accounts"`
	Login         TK2SDLogin     `json:"login"`
	LeasedTaskIDs []string       `json:"-"`
}
