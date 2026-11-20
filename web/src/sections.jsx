import { useEffect, useState } from "react";
import { api, asList, splitList } from "./api.js";

function Table({ headers, rows }) {
  return (
    <div className="table-wrap">
      <table>
        <thead>
          <tr>{headers.map((h) => <th key={h}>{h}</th>)}</tr>
        </thead>
        <tbody>
          {rows.map((cells, i) => (
            <tr key={i}>{cells.map((cell, j) => <td key={j}>{cell}</td>)}</tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function report(setMessage, err) {
  setMessage({ kind: "error", text: err.status === 401 ? "请先登录" : err.message });
}

export function Connect({ tick }) {
  const [listeners, setListeners] = useState(null);
  const [models, setModels] = useState([]);

  useEffect(() => {
    Promise.all([api("GET", "/listeners"), api("GET", "/providers"), api("GET", "/routes")])
      .then(([ports, providers, routes]) => {
        setListeners(ports);
        const names = [];
        asList(routes).forEach((route) => { if (route.modelMatch) names.push(route.modelMatch); });
        asList(providers).forEach((provider) => { (provider.models || []).forEach((model) => names.push(model)); });
        setModels([...new Set(names)]);
      })
      .catch(() => {});
  }, [tick]);

  const gateway = listeners && listeners.gateway;
  const base = gateway && gateway.effective ? "http://" + gateway.effective : "";
  return (
    <>
      <p className="status">自动改配置在「Agent」。这一页只列出要手填的三项，不改应用的配置文件。</p>
      <table>
        <tbody>
          <tr><th>基地址</th><td>{base || "网关未监听"}</td></tr>
          <tr><th>密钥</th><td>在「网关令牌」创建。密钥只显示一次，形如一串网关令牌。</td></tr>
          <tr><th>模型</th><td>{models.length ? models.join(", ") : "还没有可提供的模型。先添加提供方或路由，再接入。"}</td></tr>
        </tbody>
      </table>
    </>
  );
}

const agentState = {
  absent: "未安装",
  off: "未接入",
  on: "已接入",
  drift: "配置已变化",
  hand: "已指向网关",
  unknown: "配置不可读",
  manual: "需手动",
};

function agentError(err) {
  switch (err.message) {
    case "no_models":
      return "还没有可提供的模型。先添加提供方或路由，再接入。";
    case "not_listening":
      return "网关没有在监听，没有改应用的配置。";
    case "unknown":
      return "应用的配置读不出来，没有改它。";
    case "absent":
      return "没有检测到这个应用。";
    case "unsupported":
      return "这个应用要在它自己的界面里填写网关地址。";
    case "not_connected":
      return "这个应用不是从这里接入的，没有改它的配置。";
    case "id_taken":
      return "这些模型名已经在这个应用的配置里，没有改它。";
    default:
      return err.status === 401 ? "请先登录" : "操作没有完成";
  }
}

export function Agents({ tick, setMessage }) {
  const [rows, setRows] = useState([]);
  const [gateway, setGateway] = useState("");
  const [busy, setBusy] = useState("");
  const [known, setKnown] = useState(true);

  function load() {
    api("GET", "/agents").then((body) => {
      if (body.status !== "ok") {
        setKnown(false);
        setRows([]);
        return;
      }
      setKnown(true);
      setGateway(body.gateway || "");
      setRows(asList(body.agents));
    }).catch((err) => report(setMessage, err));
  }

  useEffect(() => { load(); }, [tick]);

  async function run(id, action) {
    setBusy(id + action);
    setMessage(null);
    try {
      await api("POST", "/agents/" + id + "/" + action);
      const again = action === "connect" ? "已接入。请重新打开这个应用，之后它的模型请求会发给网关。" : "已断开，应用配置已恢复到接入前。";
      setMessage({ kind: "note", text: again });
      load();
    } catch (err) {
      setMessage({ kind: "error", text: agentError(err) });
    } finally {
      setBusy("");
    }
  }

  if (!known) {
    return <p className="status">读不到这台电脑上的用户目录，没有检测应用。</p>;
  }

  const table = rows.map((row) => {
    const actions = [];
    if (row.support === "wire" && row.installed && row.state !== "unknown") {
      if (row.state === "on") {
        actions.push(["disconnect", "断开"]);
      } else if (row.state === "drift") {
        actions.push(["connect", "重新接入"]);
        actions.push(["disconnect", "断开"]);
      } else if (row.state === "off" || row.state === "hand") {
        actions.push(["connect", "接入"]);
      }
    }
    return [
      row.name,
      agentState[row.state] || row.state,
      <>
        <div>{row.where || "—"}</div>
        {row.model ? <div className="status">模型 {row.model}</div> : null}
        {row.note ? <div className="status">{row.note}</div> : null}
      </>,
      <span className="row-actions">
        {actions.map(([action, label]) => (
          <button key={action} type="button" className={action === "disconnect" ? "danger" : ""} disabled={busy !== ""} onClick={() => run(row.id, action)}>
            {busy === row.id + action ? "处理中" : label}
          </button>
        ))}
      </span>,
    ];
  });

  return (
    <>
      <p className="status">检测本机已安装的 coding agent。接入会改那个应用自己的配置，把模型请求指到网关，并记下原来的值。断开时只把这些值改回去。有的应用要重新打开才会读到新配置。</p>
      <p className="status">网关地址：{gateway || "网关未监听"}</p>
      <Table headers={["应用", "状态", "配置", "操作"]} rows={table} />
    </>
  );
}

export function Listeners({ tick, refresh, setMessage }) {
  const [data, setData] = useState(null);
  const [gateway, setGateway] = useState("");
  const [consoleAddr, setConsoleAddr] = useState("");
  const [share, setShare] = useState(false);

  useEffect(() => {
    api("GET", "/listeners").then((body) => {
      setData(body);
      setShare(!!body.share);
    }).catch((err) => report(setMessage, err));
  }, [tick]);

  async function save(event) {
    event.preventDefault();
    const body = { share };
    if (gateway.trim()) body.gateway = gateway.trim();
    if (consoleAddr.trim()) body.console = consoleAddr.trim();
    try {
      await api("PUT", "/listeners", body);
      setMessage({ kind: "note", text: "监听地址已写入。若地址变了，请用新的控制台地址打开此页。" });
      setGateway("");
      setConsoleAddr("");
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  const rows = data ? ["gateway", "console"].map((name) => {
    const port = data[name];
    const label = name === "gateway" ? "网关" : "控制台";
    return [label, port.configured || "（未设置，使用默认）", port.effective || "（未监听）", port.listening ? "监听中" : "未监听"];
  }) : [];

  return (
    <>
      <Table headers={["端口", "配置值", "生效地址", "状态"]} rows={rows} />
      <form className="grid" onSubmit={save}>
        <label>网关监听<input value={gateway} placeholder="留空表示不修改网关地址" onChange={(e) => setGateway(e.target.value)} /></label>
        <label>控制台监听<input value={consoleAddr} placeholder="留空表示不修改控制台地址" onChange={(e) => setConsoleAddr(e.target.value)} /></label>
        <label>对其他机器开放网关<input type="checkbox" checked={share} onChange={(e) => setShare(e.target.checked)} /></label>
        <button type="submit">保存</button>
      </form>
    </>
  );
}

const providerProtocols = [
  {
    value: "",
    name: "与调用方相同",
    example: "https://api.openai.com/v1",
    detail: "不填协议时提供方使用调用方的协议。调用方是 Chat 时，Base URL 常写成这个地址，网关再接 /chat/completions。",
  },
  {
    value: "chat",
    name: "Chat Completions",
    example: "https://api.openai.com/v1",
    detail: "网关在这个地址后接 /chat/completions。",
  },
  {
    value: "responses",
    name: "Responses",
    example: "https://api.openai.com/v1",
    detail: "网关在这个地址后接 /responses。",
  },
  {
    value: "anthropic",
    name: "Anthropic Messages",
    example: "https://api.anthropic.com",
    detail: "网关在这个地址后接 /v1/messages。",
  },
  {
    value: "gemini",
    name: "Gemini",
    example: "https://generativelanguage.googleapis.com",
    detail: "网关在这个地址后接 /v1beta/models/{模型}:generateContent。",
  },
  {
    value: "ollama",
    name: "Ollama",
    example: "http://127.0.0.1:11434",
    detail: "网关在这个地址后接 /api/chat。",
  },
];

function probeText(result) {
  if (result.ok) {
    return `可用。已用 ${result.protocol || "chat"} 向模型 ${result.model} 发了一条最短请求，提供方返回 ${result.status}。`;
  }
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
  if (!why && typeof result.error === "string" && result.error.startsWith("http_")) {
    why = `提供方返回 HTTP ${result.error.slice(5)}`;
  }
  if (!why) why = "测试没有完成";
  const where = result.model ? `模型 ${result.model}` : "这个提供方";
  return `不可用。${where}：${why}。`;
}

function canonicalProtocol(value) {
  switch ((value || "").trim().toLowerCase()) {
    case "":
      return "";
    case "chat":
    case "openai":
    case "openai-chat":
    case "chat-completions":
      return "chat";
    case "responses":
    case "openai-responses":
      return "responses";
    case "anthropic":
    case "messages":
      return "anthropic";
    case "gemini":
      return "gemini";
    case "ollama":
    case "ollama-chat":
      return "ollama";
    default:
      return value;
  }
}

export function Providers({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [editing, setEditing] = useState(null);
  const [name, setName] = useState("");
  const [protocol, setProtocol] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [models, setModels] = useState("");
  const [hidden, setHidden] = useState("");
  const [textOnly, setTextOnly] = useState("");
  const [images, setImages] = useState("");
  const [videos, setVideos] = useState("");
  const [keys, setKeys] = useState([""]);
  const [savedKeys, setSavedKeys] = useState(0);
  const [checks, setChecks] = useState([]);
  const [busy, setBusy] = useState("");
  const [testing, setTesting] = useState("");

  useEffect(() => {
    api("GET", "/providers").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
  }, [tick]);

  function edit(row) {
    setEditing(row.id);
    setName(row.name || "");
    setProtocol(canonicalProtocol(row.protocol || ""));
    setBaseUrl(row.baseUrl || "");
    setModels((row.models || []).filter((id) => !kindOf(row, id)).join(", "));
    setHidden(idsWith(row, (info) => info.hidden).join(", "));
    setTextOnly(idsWith(row, (info) => info.vision === false).join(", "));
    setImages(idsWith(row, (info) => info.kind === "image").join(", "));
    setVideos(idsWith(row, (info) => info.kind === "video").join(", "));
    setKeys([""]);
    setSavedKeys(row.keyCount || 0);
    setChecks([]);
  }

  function clearForm() {
    setEditing(null);
    setName("");
    setProtocol("");
    setBaseUrl("");
    setModels("");
    setHidden("");
    setTextOnly("");
    setImages("");
    setVideos("");
    setKeys([""]);
    setSavedKeys(0);
    setChecks([]);
  }

  function toolBody() {
    const body = {
      id: editing || "",
      baseUrl: baseUrl.trim(),
      protocol: protocol.trim(),
      credentials: keys.map((key) => key.trim()).filter(Boolean),
      models: splitList(models),
    };
    return body;
  }

  async function save(event) {
    event.preventDefault();
    const chat = splitList(models);
    const imageIds = splitList(images);
    const videoIds = splitList(videos);
    const fresh = keys.map((key) => key.trim()).filter(Boolean);
    const body = {
      name: name.trim(),
      protocol: protocol.trim(),
      baseUrl: baseUrl.trim(),
      models: [...new Set([...chat, ...imageIds, ...videoIds, ...splitList(hidden), ...splitList(textOnly)])],
      info: modelInfo(chat, splitList(hidden), splitList(textOnly), imageIds, videoIds),
    };
    try {
      if (editing) {
        if (fresh.length) body.addCredentials = fresh;
        await api("PUT", "/providers/" + editing, body);
      } else {
        body.credentials = fresh;
        await api("POST", "/providers", body);
      }
      clearForm();
      setMessage(null);
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  async function detect() {
    setBusy("detect");
    try {
      const result = await api("POST", "/providers/detect", toolBody());
      if (result.error) {
        setMessage({ kind: "error", text: probeText({ error: result.error }) });
        return;
      }
      setProtocol(canonicalProtocol(result.protocol || ""));
      setMessage({ kind: "note", text: `检测为 ${result.protocol}。OpenAI 的模型目录会计成 Chat Completions，Responses 需要自己改选。` });
    } catch (err) {
      report(setMessage, err);
    } finally {
      setBusy("");
    }
  }

  async function fetchModels() {
    setBusy("models");
    try {
      const result = await api("POST", "/providers/models", toolBody());
      if (result.error) {
        setMessage({ kind: "error", text: probeText({ error: result.error }) });
        return;
      }
      setModels((result.models || []).join(", "));
      setMessage({ kind: "note", text: `获取到 ${(result.models || []).length} 个模型。` });
    } catch (err) {
      report(setMessage, err);
    } finally {
      setBusy("");
    }
  }

  async function checkModels() {
    setBusy("check");
    setChecks([]);
    try {
      const result = await api("POST", "/providers/check", toolBody());
      const rows = result.results || [];
      setChecks(rows);
      const ok = rows.filter((row) => row.ok).length;
      setMessage({ kind: ok === rows.length && rows.length ? "note" : "error", text: `检验了 ${rows.length} 个模型，${ok} 个可用。` });
    } catch (err) {
      report(setMessage, err);
    } finally {
      setBusy("");
    }
  }

  async function removeSavedKey(index) {
    try {
      await api("DELETE", "/providers/" + editing + "/keys/" + index);
      setSavedKeys((n) => Math.max(0, n - 1));
      setMessage({ kind: "note", text: "已删除一把密钥。" });
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  const knownProtocol = providerProtocols.some((item) => item.value === protocol);
  const selected = providerProtocols.find((item) => item.value === protocol) || {
    value: protocol,
    name: protocol,
    example: "",
    detail: "已保存的协议不在列表中。",
  };

  return (
    <>
      <form className="grid" onSubmit={save}>
        <p className="form-note">一个提供方有一个名称。同一协议可以保存多把密钥。新建时名称和至少一把密钥必填。协议、Base URL 和模型可以留空。协议留空表示与调用方相同，检测时按各协议的目录地址试一次。</p>
        <label>名称<input value={name} placeholder="例如 My Relay" onChange={(e) => setName(e.target.value)} required /></label>
        <label>协议
          <select value={protocol} onChange={(e) => setProtocol(e.target.value)}>
            {knownProtocol ? null : <option value={protocol}>{protocol}</option>}
            {providerProtocols.map((item) => <option key={item.value || "same"} value={item.value}>{item.name}</option>)}
          </select>
        </label>
        <label>Base URL<input value={baseUrl} placeholder={selected.example || "上游地址"} onChange={(e) => setBaseUrl(e.target.value)} /></label>
        <p className="form-note">示例：{selected.example ? <code>{selected.example}</code> : "无"}。{selected.detail}</p>
        <div className="keys">
          <span>密钥{editing ? `（已保存 ${savedKeys} 把，留空则保持）` : "（至少一把）"}</span>
          {editing ? Array.from({ length: savedKeys }, (_, index) => (
            <div className="key-row" key={"saved-" + index}>
              <input value={"已保存的密钥 " + (index + 1)} readOnly />
              <button type="button" className="danger" onClick={() => removeSavedKey(index)}>删除</button>
            </div>
          )) : null}
          {keys.map((key, index) => (
            <div className="key-row" key={"new-" + index}>
              <input type="password" value={key} placeholder="粘贴 API 密钥" onChange={(e) => setKeys(keys.map((item, i) => i === index ? e.target.value : item))} />
              {keys.length > 1 ? <button type="button" className="ghost" onClick={() => setKeys(keys.filter((_, i) => i !== index))}>去掉</button> : null}
            </div>
          ))}
          <button type="button" className="ghost" onClick={() => setKeys([...keys, ""])}>再加一把密钥</button>
        </div>
        <label>对话模型<input value={models} placeholder="用逗号分隔，例如 gpt-5.5, claude-sonnet-5" onChange={(e) => setModels(e.target.value)} /></label>
        <div className="row-actions">
          <button type="button" className="ghost" disabled={busy !== ""} onClick={detect}>{busy === "detect" ? "检测中" : "检测协议"}</button>
          <button type="button" className="ghost" disabled={busy !== ""} onClick={fetchModels}>{busy === "models" ? "获取中" : "获取模型"}</button>
          <button type="button" className="ghost" disabled={busy !== ""} onClick={checkModels}>{busy === "check" ? "检验中" : "逐个检验"}</button>
        </div>
        {checks.length ? <ul className="checks">{checks.map((row) => <li key={row.model || row.error} className={row.ok ? "ok" : "bad"}>{row.ok ? `${row.model}：可用 ${row.status}` : probeText(row)}</li>)}</ul> : null}
        <label>隐藏模型<input value={hidden} placeholder="不出现在目录里，也不能调用" onChange={(e) => setHidden(e.target.value)} /></label>
        <label>不接受图片<input value={textOnly} placeholder="带图片或视频的请求会被拒绝" onChange={(e) => setTextOnly(e.target.value)} /></label>
        <label>图片模型<input value={images} placeholder="不进对话目录" onChange={(e) => setImages(e.target.value)} /></label>
        <label>视频模型<input value={videos} placeholder="不进对话目录" onChange={(e) => setVideos(e.target.value)} /></label>
        <button type="submit">{editing ? "更新提供方" : "添加提供方"}</button>
      </form>
      <Table headers={["名称", "ID", "协议", "Base URL", "模型", "密钥", ""]} rows={list.map((p) => [
        p.name || p.id, p.id, p.protocol, p.baseUrl, (p.models || []).join(", "), String(p.keyCount || 0),
        <span className="row-actions" key={p.id}>
          <button type="button" className="ghost" disabled={testing === p.id} onClick={async () => {
            setTesting(p.id);
            try {
              const result = await api("POST", "/providers/" + p.id + "/test");
              setMessage({ kind: result.ok ? "note" : "error", text: probeText(result) });
            } catch (err) {
              report(setMessage, err);
            } finally {
              setTesting("");
            }
          }}>{testing === p.id ? "测试中" : "测试"}</button>
          <button type="button" className="ghost" onClick={() => edit(p)}>编辑</button>
          <button type="button" className="danger" onClick={async () => {
            try { await api("DELETE", "/providers/" + p.id); refresh(); }
            catch (err) { report(setMessage, err); }
          }}>删除</button>
        </span>,
      ])} />
    </>
  );
}

export function Routes({ tick, refresh, setMessage }) {
  const [routes, setRoutes] = useState([]);
  const [providers, setProviders] = useState([]);
  const [model, setModel] = useState("");
  const [providerIds, setProviderIds] = useState("");
  const [eligible, setEligible] = useState("");
  const [limit, setLimit] = useState(false);

  useEffect(() => {
    Promise.all([api("GET", "/routes"), api("GET", "/providers")])
      .then(([routeBody, providerBody]) => {
        setRoutes(asList(routeBody));
        setProviders(asList(providerBody));
      })
      .catch((err) => report(setMessage, err));
  }, [tick]);

  async function save(event) {
    event.preventDefault();
    const body = { modelMatch: model.trim(), providerIds: splitList(providerIds) };
    if (limit) body.eligible = splitList(eligible);
    try {
      await api("POST", "/routes", body);
      setModel("");
      setProviderIds("");
      setEligible("");
      setLimit(false);
      setMessage(null);
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  return (
    <>
      <p className="status">{providers.length ? "已有提供方：" + providers.map((p) => p.id).join(", ") : "还没有提供方。路由可以先不填提供方。"}</p>
      <form className="grid" onSubmit={save}>
        <label>模型匹配<input value={model} placeholder="与请求中的模型名完全一致" onChange={(e) => setModel(e.target.value)} /></label>
        <label>提供方顺序<input value={providerIds} placeholder="提供方 ID，逗号分隔，按尝试顺序" onChange={(e) => setProviderIds(e.target.value)} /></label>
        <label>降级类别<input value={eligible} placeholder="留空则只用 upstream_unavailable" onChange={(e) => setEligible(e.target.value)} /></label>
        <label>写入 eligible（空列表表示不降级）<input type="checkbox" checked={limit} onChange={(e) => setLimit(e.target.checked)} /></label>
        <button type="submit">新建路由</button>
      </form>
      <Table headers={["ID", "模型", "提供方", "eligible", ""]} rows={routes.map((r) => [
        r.id, r.modelMatch, (r.providerIds || []).join(", "),
        r.eligible ? (r.eligible.length ? r.eligible.join(", ") : "（空，不降级）") : "（默认）",
        <button key={r.id} type="button" className="danger" onClick={async () => {
          try { await api("DELETE", "/routes/" + r.id); refresh(); }
          catch (err) { report(setMessage, err); }
        }}>删除</button>,
      ])} />
    </>
  );
}

export function Plugins({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [enabled, setEnabled] = useState(true);

  useEffect(() => {
    api("GET", "/plugins").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
  }, [tick]);

  async function create(event) {
    event.preventDefault();
    try {
      await api("POST", "/plugins", { enabled });
      setMessage(null);
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  async function reorder(ids) {
    try {
      await api("PUT", "/plugins/order", { ids });
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  return (
    <>
      <form className="grid" onSubmit={create}>
        <label>创建时启用<input type="checkbox" checked={enabled} onChange={(e) => setEnabled(e.target.checked)} /></label>
        <button type="submit">新建插件记录</button>
      </form>
      <p className="note">这里只保存启用状态和顺序。进程不会加载插件。</p>
      <Table headers={["顺序", "ID", "启用", ""]} rows={list.map((p, i) => [
        p.order, p.id, p.enabled ? "是" : "否",
        <span className="row-actions" key={p.id}>
          <button type="button" className="ghost" onClick={() => {
            if (i === 0) return;
            const ids = list.map((item) => item.id);
            [ids[i - 1], ids[i]] = [ids[i], ids[i - 1]];
            reorder(ids);
          }}>上移</button>
          <button type="button" className="ghost" onClick={() => {
            if (i === list.length - 1) return;
            const ids = list.map((item) => item.id);
            [ids[i + 1], ids[i]] = [ids[i], ids[i + 1]];
            reorder(ids);
          }}>下移</button>
          <button type="button" className="ghost" onClick={async () => {
            try { await api("PUT", "/plugins/" + p.id, { enabled: !p.enabled }); refresh(); }
            catch (err) { report(setMessage, err); }
          }}>{p.enabled ? "禁用" : "启用"}</button>
        </span>,
      ])} />
    </>
  );
}

function kindOf(row, id) {
  const info = (row.info || []).find((item) => item.id === id);
  return info && (info.kind === "image" || info.kind === "video");
}

function idsWith(row, pred) {
  return (row.info || []).filter(pred).map((info) => info.id);
}

function modelInfo(chat, hidden, textOnly, images, videos) {
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

export function Tokens({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [models, setModels] = useState("");

  useEffect(() => {
    api("GET", "/tokens").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
  }, [tick]);

  async function create(event) {
    event.preventDefault();
    try {
      const created = await api("POST", "/tokens", { models: splitList(models) });
      setModels("");
      setMessage({ kind: "secret", text: "令牌只显示这一次：" + created.secret });
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  return (
    <>
      <form className="grid" onSubmit={create}>
        <label>可用模型<input value={models} placeholder="留空表示目录里的全部模型，逗号分隔" onChange={(e) => setModels(e.target.value)} /></label>
        <button type="submit">创建网关令牌</button>
      </form>
      <Table headers={["ID", "掩码", "可用模型", "已撤销", ""]} rows={list.map((t) => [
        t.id, t.mask, (t.models || []).join(", ") || "全部", t.revoked ? "是" : "否",
        t.revoked ? "" : <button key={t.id} type="button" className="danger" onClick={async () => {
          try { await api("POST", "/tokens/" + t.id + "/revoke"); setMessage(null); refresh(); }
          catch (err) { report(setMessage, err); }
        }}>撤销</button>,
      ])} />
    </>
  );
}

function duration(record) {
  const started = Date.parse(record.started);
  const finished = Date.parse(record.finished);
  if (Number.isNaN(started) || Number.isNaN(finished)) return "";
  return Math.max(0, finished - started) + " ms";
}

function countOf(usage, keys) {
  for (let i = 0; i < keys.length; i++) {
    if (typeof usage[keys[i]] === "number") return usage[keys[i]];
  }
  return null;
}

function usageLabel(usage) {
  if (!usage || typeof usage !== "object") return "未知";
  const input = countOf(usage, ["prompt_tokens", "input_tokens", "promptTokenCount"]);
  const output = countOf(usage, ["completion_tokens", "output_tokens", "candidatesTokenCount"]);
  const total = countOf(usage, ["total_tokens", "totalTokenCount"]);
  const details = usage.prompt_tokens_details;
  const cached = details && typeof details.cached_tokens === "number"
    ? details.cached_tokens
    : typeof usage.cache_read_input_tokens === "number"
      ? usage.cache_read_input_tokens
      : null;
  const parts = [];
  if (input != null) parts.push("输入 " + input);
  if (output != null) parts.push("输出 " + output);
  if (total != null) parts.push("合计 " + total);
  if (cached != null) parts.push("缓存 " + cached);
  return parts.length ? parts.join(" · ") : JSON.stringify(usage);
}

export function Usage({ tick, setMessage }) {
  const [routeId, setRouteId] = useState("");
  const [providerId, setProviderId] = useState("");
  const [start, setStart] = useState("");
  const [end, setEnd] = useState("");
  const [body, setBody] = useState(null);

  async function load(event) {
    if (event) event.preventDefault();
    const q = new URLSearchParams();
    if (routeId.trim()) q.set("routeId", routeId.trim());
    if (providerId.trim()) q.set("providerId", providerId.trim());
    if (start.trim()) q.set("start", start.trim());
    if (end.trim()) q.set("end", end.trim());
    try {
      setBody(await api("GET", "/usage" + (q.toString() ? "?" + q : "")));
    } catch (err) {
      report(setMessage, err);
    }
  }

  useEffect(() => { load(); }, [tick]);

  const records = body && body.status !== "unknown" ? (body.records || []) : [];
  return (
    <>
      <form className="grid" onSubmit={load}>
        <label>路由<input value={routeId} onChange={(e) => setRouteId(e.target.value)} /></label>
        <label>提供方<input value={providerId} onChange={(e) => setProviderId(e.target.value)} /></label>
        <label>开始<input value={start} placeholder="RFC3339" onChange={(e) => setStart(e.target.value)} /></label>
        <label>结束<input value={end} placeholder="RFC3339" onChange={(e) => setEnd(e.target.value)} /></label>
        <button type="submit">筛选</button>
      </form>
      {body && body.status === "unknown"
        ? <p className="note">用量存储不可读，不能当成 0。</p>
        : <Table headers={["开始", "模型", "调用方", "会话", "路由", "提供方", "结果", "耗时", "用量"]} rows={records.map((r) => [
          r.started, r.model || "", r.caller || r.tokenId || "", r.session || "", r.routeId, r.providerId, r.outcome, duration(r), usageLabel(r.usage),
        ])} />}
    </>
  );
}

export function Conversations({ tick, setMessage }) {
  const [list, setList] = useState(null);
  const [session, setSession] = useState("");
  const [turns, setTurns] = useState(null);

  useEffect(() => {
    api("GET", "/conversations").then(setList).catch((err) => report(setMessage, err));
  }, [tick]);

  async function open(id) {
    setSession(id);
    try {
      setTurns(await api("GET", "/conversations/turns?session=" + encodeURIComponent(id)));
    } catch (err) {
      report(setMessage, err);
    }
  }

  const conversations = list && list.status !== "unknown" ? (list.conversations || []) : [];
  const rows = turns && turns.status !== "unknown" ? (turns.turns || []) : [];
  return (
    <>
      <p className="status">同一次对话的请求归在客户端带来的会话标识下。一次流式回复先收成一整段，再记成这一次的输出。</p>
      {list && list.status === "unknown"
        ? <p className="note">对话存档不可读，不能当成没有对话。</p>
        : <Table headers={["最近", "会话", "次数", ""]} rows={conversations.map((item) => [
          item.last, item.session, item.turns,
          <button key={item.session} type="button" className="ghost" onClick={() => open(item.session)}>查看</button>,
        ])} />}
      {session && turns && turns.status === "unknown" ? <p className="note">这段对话不可读。</p> : null}
      {session && rows.length ? rows.map((turn, i) => (
        <article key={session + i} className="turn">
          <p className="note">{turn.at} · {turn.model || ""} · {turn.caller || ""}</p>
          {turn.input ? <p className="turn-text"><strong>输入</strong> {turn.input}</p> : null}
          <p className="turn-text"><strong>输出</strong> {turn.output}</p>
        </article>
      )) : null}
    </>
  );
}
