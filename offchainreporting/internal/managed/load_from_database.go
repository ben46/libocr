package managed

import (
	"context"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
)

// loadConfigFromDatabase 从数据库中加载配置信息
func loadConfigFromDatabase(ctx context.Context, database types.Database, logger loghelper.LoggerWithContext) *types.ContractConfig {
	// 从数据库中读取配置信息
	cc, err := database.ReadConfig(ctx)
	if err != nil {
		// 记录错误日志
		logger.ErrorIfNotCanceled("loadConfigFromDatabase: Database.ReadConfig 出错", ctx, commontypes.LogFields{
			"error": err,
		})
		return nil
	}

	if cc == nil {
		// 如果配置信息为 nil，则记录信息日志
		logger.Info("loadConfigFromDatabase: Database.ReadConfig 返回 nil，无需恢复配置", nil)
		return nil
	}

	return cc
}