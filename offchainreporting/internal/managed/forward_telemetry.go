package managed

import (
	"context"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/serialization/protobuf"
	"google.golang.org/protobuf/proto"
)

// forwardTelemetry 接收 chTelemetry 上的监控事件，将其序列化并转发到 monitoringEndpoint
func forwardTelemetry(
	ctx context.Context,
	logger loghelper.LoggerWithContext,
	monitoringEndpoint commontypes.MonitoringEndpoint,
	chTelemetry <-chan *protobuf.TelemetryWrapper,
) {
	for {
		select {
		case t, ok := <-chTelemetry:
			if !ok {
				// 这不应该发生，但我们仍然优雅地处理这种情况，以防万一...
				logger.Error("forwardTelemetry: chTelemetry 意外关闭。退出", nil)
				return
			}
			bin, err := proto.Marshal(t)
			if err != nil {
				logger.Error("forwardTelemetry: 失败 Marshal protobuf", commontypes.LogFields{
					"proto": t,
					"error": err,
				})
				break
			}
			if monitoringEndpoint != nil {
				monitoringEndpoint.SendLog(bin)
			}
		case <-ctx.Done():
			logger.Info("forwardTelemetry: 退出", nil)
			return
		}
	}
}