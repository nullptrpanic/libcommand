CHAT_ID="oc_28ebbd1168a2173f48bb23364a2d88fe"

# 第1-10条
for i in $(seq 1 10); do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "评测任务已启动" --as bot
 sleep 0.5
done

# 第11-20条
for i in $(seq 1 10); do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "请各成员确认执行计划" --as bot
 sleep 0.5
done

# 第21-30条
for i in $(seq 1 10); do
 lark-cli im +messages-send --chat-id "$CHAT_ID" --text "请在今天下班前反馈风险项" --as bot
 sleep 0.5
done

echo "全部30条消息发送完成"
