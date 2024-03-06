package managed

import (
	"context"
	"math/rand"
	"time"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
)

const collectInterval = 10 * time.Minute
const olderThan = 24 * time.Hour

// collectGarbage 定期收集由旧传输协议实例留下的垃圾
func collectGarbage(
	ctx context.Context,
	database types.Database,
	localConfig types.LocalConfig,
	logger loghelper.LoggerWithContext,
) {
	for {
		// 随机休眠一小段时间，以分散垃圾收集的时间
		wait := collectInterval + time.Duration(rand.Float64()*5.0*60.0)*time.Second
		logger.Info("collectGarbage: 准备进入休眠状态", commontypes.LogFields{
			"duration": wait,
		})
		select {
		case <-time.After(wait):
			logger.Info("collectGarbage: 开始清理旧传输", commontypes.LogFields{
				"olderThan": olderThan,
			})
			// 为了确保不泄露上下文，我们对数据库查询进行了包装
			func() {
				childCtx, childCancel := context.WithTimeout(ctx, localConfig.DatabaseTimeout)
				defer childCancel()
				err := database.DeletePendingTransmissionsOlderThan(childCtx, time.Now().Add(-olderThan))
				if err != nil {
					logger.Info("collectGarbage: DeletePendingTransmissionsOlderThan 出错", commontypes.LogFields{
						"error":     err,
						"olderThan": olderThan,
					})
				} else {
					logger.Info("collectGarbage: 清理完成", nil)
				}
			}()
		case <-ctx.Done():
			logger.Info("collectGarbage: 退出", nil)
			return
		}
	}
}
