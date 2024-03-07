package protocol

import (
	"context"

	"github.com/smartcontractkit/libocr/commontypes"
	"github.com/smartcontractkit/libocr/internal/loghelper"
	"github.com/smartcontractkit/libocr/offchainreporting/internal/config"
	"github.com/smartcontractkit/libocr/offchainreporting/types"
	"github.com/smartcontractkit/libocr/subprocesses"
)

const futureMessageBufferSize = 10 // big enough for a couple of full rounds of repgen protocol

// RunOracle 运行 offchain 报告协议的一个 Oracle 实例，并管理所有底层 goroutine 的生命周期。
//
// RunOracle 会一直运行直到 ctx 被取消。只有在所有子 goroutine 退出后才会关闭。

func RunOracle(
	ctx context.Context,

	config config.SharedConfig,
	configOverrider types.ConfigOverrider,
	contractTransmitter types.ContractTransmitter,
	database types.Database,
	datasource types.DataSource,
	id commontypes.OracleID,
	keys types.PrivateKeys,
	localConfig types.LocalConfig,
	logger loghelper.LoggerWithContext,
	netEndpoint NetworkEndpoint,
	telemetrySender TelemetrySender,
) {
	o := oracleState{
		ctx: ctx,

		Config:              config,
		configOverrider:     configOverrider,
		contractTransmitter: contractTransmitter,
		database:            database,
		datasource:          datasource,
		id:                  id,
		localConfig:         localConfig,
		logger:              logger,
		netEndpoint:         netEndpoint,
		PrivateKeys:         keys,
		telemetrySender:     telemetrySender,
	}
	o.run()
}

type oracleState struct {
	ctx context.Context

	Config              config.SharedConfig
	configOverrider     types.ConfigOverrider
	contractTransmitter types.ContractTransmitter
	database            types.Database
	datasource          types.DataSource
	id                  commontypes.OracleID
	localConfig         types.LocalConfig
	logger              loghelper.LoggerWithContext
	netEndpoint         NetworkEndpoint
	PrivateKeys         types.PrivateKeys
	telemetrySender     TelemetrySender

	bufferedMessages        []*MessageBuffer
	chNetToPacemaker        chan<- MessageToPacemakerWithSender
	chNetToReportGeneration chan<- MessageToReportGenerationWithSender
	childCancel             context.CancelFunc
	childCtx                context.Context
	epoch                   uint32
	subprocesses            subprocesses.Subprocesses
}

// run 确保在 o.ctx.Done() 被关闭时安全关闭 Oracle 的“子例程”（Pacemaker、ReportGeneration 和 Transmission）。
//
// 这里是涉及的各种通道以及它们传输内容的图表。
//
//	    ┌─────────────epoch changes─────────────┐
//	    ▼                                       │
//	┌──────┐                               ┌────┴────┐
//	│Oracle├────pacemaker messages────────►│Pacemaker│
//	└────┬─┘                               └─────────┘
//	     │                                       ▲
//	     └──────rep. gen. messages────────────┐  │
//	                                          ▼  │progress events
//	┌────────────┐                         ┌─────┴──────────┐
//	│Transmission│◄──────reports───────────┤ReportGeneration│
//	└────────────┘                         └────────────────┘
//
// 所有通道都是无缓冲的。
//
// 一旦 o.ctx.Done() 被关闭，Oracle 的运行循环将进入相应的 select case，并不再将网络消息转发给 Pacemaker 和 ReportGeneration。
// 然后将取消 o.childCtx，使所有子例程退出。
// 为了防止死锁，在 Oracle、Pacemaker、ReportGeneration、Transmission 等地方的所有通道发送和接收操作都包含在 select{} 语句中，同时还包含了一个用于取消上下文的 case。
//
// 最后，在协议中生成的所有子 goroutine 都附加到 o.subprocesses（ReportGeneration 除外，它是由 Pacemaker 显式管理的）。这使我们能够在退出之前等待它们完成。
func (o *oracleState) run() {
	o.logger.Info("Running", nil)

	for i := 0; i < o.Config.N(); i++ {
		o.bufferedMessages = append(o.bufferedMessages, NewMessageBuffer(futureMessageBufferSize))
	}

	chNetToPacemaker := make(chan MessageToPacemakerWithSender)
	o.chNetToPacemaker = chNetToPacemaker

	chNetToReportGeneration := make(chan MessageToReportGenerationWithSender)
	o.chNetToReportGeneration = chNetToReportGeneration

	chPacemakerToOracle := make(chan uint32)

	chReportGenerationToTransmission := make(chan EventToTransmission)

	o.childCtx, o.childCancel = context.WithCancel(context.Background())
	defer o.childCancel()

	o.subprocesses.Go(func() {
		RunPacemaker(
			o.childCtx,
			&o.subprocesses,

			chNetToPacemaker,
			chNetToReportGeneration,
			chPacemakerToOracle,
			chReportGenerationToTransmission,
			o.Config,
			o.configOverrider,
			o.contractTransmitter,
			o.database,
			o.datasource,
			o.id,
			o.localConfig,
			o.logger,
			o.netEndpoint,
			o.PrivateKeys,
			o.telemetrySender,
		)
	})
	o.subprocesses.Go(func() {
		RunTransmission(
			o.childCtx,
			&o.subprocesses,

			o.Config,
			o.configOverrider,
			chReportGenerationToTransmission,
			o.database,
			o.id,
			o.localConfig,
			o.logger,
			o.contractTransmitter,
		)
	})

	chNet := o.netEndpoint.Receive()

	chDone := o.ctx.Done()
	for {
		select {
		case msg := <-chNet:
			// This bounds check should never trigger since it's the netEndpoint's
			// responsibility to only provide valid senders. We perform it for
			// defense-in-depth.
			if 0 <= int(msg.Sender) && int(msg.Sender) < o.Config.N() {
				msg.Msg.process(o, msg.Sender)
			} else {
				o.logger.Critical("msg.Sender out of bounds. This should *never* happen.", commontypes.LogFields{
					"sender": msg.Sender,
					"n":      o.Config.N(),
				})
			}
		case epoch := <-chPacemakerToOracle:
			o.epochChanged(epoch)
		case <-chDone:
		}

		// ensure prompt exit
		select {
		case <-chDone:
			o.logger.Debug("Oracle: winding down", nil)
			o.childCancel()
			o.subprocesses.Wait()
			o.logger.Debug("Oracle: exiting", nil)
			return
		default:
		}
	}
}

func (o *oracleState) epochChanged(e uint32) {
	o.epoch = e
	o.logger.Trace("epochChanged: getting messages for new epoch", commontypes.LogFields{
		"epoch": e,
	})
	for id, buffer := range o.bufferedMessages {
		for {
			msg := buffer.Peek()
			if msg == nil {
				// no messages left in buffer
				break
			}
			msgEpoch := (*msg).epoch()
			if msgEpoch < e {
				// remove from buffer
				buffer.Pop()
				o.logger.Debug("epochChanged: unbuffered and dropped message", commontypes.LogFields{
					"remoteOracleID": id,
					"epoch":          e,
					"message":        *msg,
				})
			} else if msgEpoch == e {
				// remove from buffer
				buffer.Pop()

				o.logger.Trace("epochChanged: unbuffered messages for new epoch", commontypes.LogFields{
					"remoteOracleID": id,
					"epoch":          e,
					"message":        *msg,
				})
				o.chNetToReportGeneration <- MessageToReportGenerationWithSender{
					*msg,
					commontypes.OracleID(id),
				}
			} else { // msgEpoch > e
				// this and all subsequent messages are for future epochs
				// leave them in the buffer
				break
			}
		}
	}
	o.logger.Trace("epochChanged: done getting messages for new epoch", commontypes.LogFields{
		"epoch": e,
	})
}

func (o *oracleState) reportGenerationMessage(msg MessageToReportGeneration, sender commontypes.OracleID) {
	msgEpoch := msg.epoch()
	if msgEpoch < o.epoch {
		// drop
		o.logger.Debug("oracle: dropping message for past epoch", commontypes.LogFields{
			"epoch":  o.epoch,
			"sender": sender,
			"msg":    msg,
		})
	} else if msgEpoch == o.epoch {
		o.chNetToReportGeneration <- MessageToReportGenerationWithSender{msg, sender}
	} else {
		o.bufferedMessages[sender].Push(msg)
		o.logger.Trace("oracle: buffering message for future epoch", commontypes.LogFields{
			"epoch":  o.epoch,
			"sender": sender,
			"msg":    msg,
		})
	}
}
