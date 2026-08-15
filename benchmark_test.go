package libcommand

import (
	"context"
	"testing"
)

const thirtyCommandScript = `
CHAT_ID="oc_28ebbd1168a2173f48bb23364a2d88fe"

for i in {1..10}; do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "评测任务已启动" --as bot
done

for i in {1..10}; do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "请各成员确认执行计划" --as bot
done

for i in {1..10}; do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "请在今天下班前反馈风险项" --as bot
done
`

func BenchmarkSimulatorThirtyCommands(b *testing.B) {
	callbacks := 0
	simulator := mustBuildSimulator(b, "lark-cli", countInvocations(&callbacks))
	request := &SimulationRequest{Source: thirtyCommandScript}

	if err := simulator.Simulate(context.Background(), request); err != nil {
		b.Fatal(err)
	}
	if callbacks != 30 {
		b.Fatalf("preflight callbacks = %d", callbacks)
	}
	callbacks = 0
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		if err := simulator.Simulate(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if want := 30 * b.N; callbacks != want {
		b.Fatalf("callbacks = %d, want %d", callbacks, want)
	}
}
