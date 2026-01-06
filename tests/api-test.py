import os
from openai import OpenAI

# 请确保您已将 API Key 存储在环境变量 WQ_API_KEY 中
# 初始化 OpenAI 客户端，从环境变量中读取您的 API Key
client = OpenAI(
    # 此为默认路径，您可根据业务所在地域进行配置
    base_url="https://wanqing.streamlakeapi.com/api/gateway/v1/endpoints",
    # 从环境变量中获取您的 API Key
    api_key=os.environ.get("WQ_API_KEY")
)

# Single-round:
print("----- standard request -----")
completion = client.chat.completions.create(
    # model="KAT-Coder-Air-V1"
    model="ep-w1klde-1767706500222997285", 
    messages=[
        {"role": "system", "content": "你是一个 AI 人工智能助手"},
        {"role": "user", "content": "请介绍一下太阳系的八大行星"},
    ],
)
print(completion.choices[0].message.content)
