package facts

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/vshivtsev-dev/keel/internal/exec"
)

// BackupJob — задание vzdump, заведённое на хосте.
//
// Поля приходят из pvesh как есть; часть булевых значений Proxmox отдаёт
// числами, поэтому они читаются как json.Number и сравниваются по строке.
type BackupJob struct {
	ID       string `json:"id"`
	Comment  string `json:"comment"`
	Schedule string `json:"schedule"`
	Storage  string `json:"storage"`
	Mode     string `json:"mode"`
	VMID     string `json:"vmid"`
	All      flag   `json:"all"`
	Enabled  flag   `json:"enabled"`
	Compress string `json:"compress"`
	Prune    string `json:"prune-backups"`
}

// flag читает и 1, и "1", и true — Proxmox отдаёт их вперемешку.
type flag bool

func (f *flag) UnmarshalJSON(raw []byte) error {
	s := strings.Trim(string(raw), `"`)
	*f = s == "1" || s == "true"
	return nil
}

func (f flag) Bool() bool { return bool(f) }

// BackupJobs спрашивает у Proxmox список заданий.
func (f *Facts) collectBackupJobs(ctx context.Context, c exec.Capturer) {
	if !c.Has("pvesh") {
		return
	}
	out, err := c.Capture(ctx, "pvesh", "get", "/cluster/backup", "--output-format", "json")
	if err != nil {
		return
	}
	var jobs []BackupJob
	if err := json.Unmarshal([]byte(out), &jobs); err != nil {
		return
	}
	f.BackupJobs = jobs
}

// BackupJobBy находит задание по комментарию-метке. Именно по ней keel и
// узнаёт своё задание при повторных запусках.
func (f *Facts) BackupJobBy(comment string) *BackupJob {
	for i := range f.BackupJobs {
		if f.BackupJobs[i].Comment == comment {
			return &f.BackupJobs[i]
		}
	}
	return nil
}
