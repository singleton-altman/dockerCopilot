package utiles

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/onlyLTY/dockerCopilot/internal/svc"
)

func BackupList(ctx *svc.ServiceContext) ([]string, error) {
	var backupList []string
	dir := os.Getenv("BACKUP_DIR") // 从环境变量中获取备份目录
	if dir == "" {
		dir = "/data/backups" // 如果环境变量未设置，使用默认值
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		// 还没有创建过任何备份时目录并不存在，这是正常状态而不是错误。
		if errors.Is(err, os.ErrNotExist) {
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
