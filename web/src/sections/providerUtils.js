export const providerProtocols = [
  { value: "", name: "与调用方相同", example: "https://api.openai.com/v1", detail: "提供方使用调用方的协议。调用方是 Chat 时，网关会在这个地址后接 /chat/completions。" },
  { value: "chat", name: "Chat Completions", example: "https://api.openai.com/v1", detail: "网关在这个地址后接 /chat/completions。" },
  { value: "responses", name: "Responses", example: "https://api.openai.com/v1", detail: "网关在这个地址后接 /responses。" },
  { value: "anthropic", name: "Anthropic Messages", example: "https://api.anthropic.com", detail: "网关在这个地址后接 /v1/messages。" },
  { value: "gemini", name: "Gemini", example: "https://generativelanguage.googleapis.com", detail: "网关在这个地址后接 /v1beta/models/{模型}:generateContent。" },
  { value: "ollama", name: "Ollama", example: "http://127.0.0.1:11434", detail: "网关在这个地址后接 /api/chat。" },
];

export function probeText(result) {
  if (result.ok) return `可用。已用 ${result.protocol || "chat"} 向模型 ${result.model} 发了一条最短请求，提供方返回 ${result.status}。`;
  const named = {
    auth_failure: "提供方拒绝了密钥",
    rate_limit: "提供方要求放慢请求",
    empty_balance: "提供方没有余额",
    upstream_unavailable: "连不上提供方",
    base_url: "Base URL 不能拨号",
    unknown: "协议无法识别，或 Base URL 为空",
    model: "没有可测试的对话模型",
    credential: "没有密钥",
  };
  let why = named[result.error];
  if (!why && typeof result.error === "string" && result.error.startsWith("http_")) why = `提供方返回 HTTP ${result.error.slice(5)}`;
  const where = result.model ? `模型 ${result.model}` : "这个提供方";
  return `不可用。${where}：${why || "测试没有完成"}。`;
}

export function canonicalProtocol(value) {
  switch ((value || "").trim().toLowerCase()) {
    case "": return "";
    case "chat": case "openai": case "openai-chat": case "chat-completions": return "chat";
    case "responses": case "openai-responses": return "responses";
    case "anthropic": case "messages": return "anthropic";
    case "gemini": return "gemini";
    case "ollama": case "ollama-chat": return "ollama";
    default: return value;
  }
}

export function kindOf(row, id) {
  const info = (row.info || []).find((item) => item.id === id);
  return info && (info.kind === "image" || info.kind === "video");
}

export function idsWith(row, predicate) {
  return (row.info || []).filter(predicate).map((info) => info.id);
}

export function modelInfo(chat, hidden, textOnly, images, videos) {
  const ids = [...new Set([...chat, ...hidden, ...textOnly, ...images, ...videos])];
  return ids.map((id) => {
    const info = { id };
    if (hidden.includes(id)) info.hidden = true;
    if (textOnly.includes(id)) info.vision = false;
    if (images.includes(id)) info.kind = "image";
    if (videos.includes(id)) info.kind = "video";
    return info;
  }).filter((info) => info.hidden || info.vision === false || info.kind);
}
