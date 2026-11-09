package utiles

import (
	"os"
	"path/filepath"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
)

func BackupList(ctx *svc.ServiceContext) ([]string, error) {
	var backupList []string
	dir := BackupDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// 全新安装还没有备份目录：视为空列表，
			// 而不是把 "open /data/backups: no such file or directory" 抛给页面
			return backupList, nil
		}
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && filepath.Ext(entry.Name()) == ".json" {
			backupList = append(backupList, entry.Name())
		} else if !entry.IsDir() && filepath.Ext(entry.Name()) == ".yaml" {
			backupList = append(backupList, entry.Name())
		}
	}

	return backupList, nil
}
