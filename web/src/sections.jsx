import { useEffect, useState } from "react";
import { api, asList, splitList } from "./api.js";

function Table({ headers, rows }) {
  return (
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
      <p className="status">把应用自己的基地址、密钥和模型列表改成下面三项。这里不改应用的配置文件。</p>
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

export function Providers({ tick, refresh, setMessage }) {
  const [list, setList] = useState([]);
  const [editing, setEditing] = useState(null);
  const [protocol, setProtocol] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [models, setModels] = useState("");
  const [hidden, setHidden] = useState("");
  const [textOnly, setTextOnly] = useState("");
  const [images, setImages] = useState("");
  const [videos, setVideos] = useState("");
  const [credential, setCredential] = useState("");

  useEffect(() => {
    api("GET", "/providers").then((body) => setList(asList(body))).catch((err) => report(setMessage, err));
  }, [tick]);

  function edit(row) {
    setEditing(row.id);
    setProtocol(row.protocol || "");
    setBaseUrl(row.baseUrl || "");
    setModels((row.models || []).filter((id) => !kindOf(row, id)).join(", "));
    setHidden(idsWith(row, (info) => info.hidden).join(", "));
    setTextOnly(idsWith(row, (info) => info.vision === false).join(", "));
    setImages(idsWith(row, (info) => info.kind === "image").join(", "));
    setVideos(idsWith(row, (info) => info.kind === "video").join(", "));
    setCredential("");
  }

  async function save(event) {
    event.preventDefault();
    const chat = splitList(models);
    const imageIds = splitList(images);
    const videoIds = splitList(videos);
    const body = {
      protocol: protocol.trim(),
      baseUrl: baseUrl.trim(),
      models: [...new Set([...chat, ...imageIds, ...videoIds, ...splitList(hidden), ...splitList(textOnly)])],
      info: modelInfo(chat, splitList(hidden), splitList(textOnly), imageIds, videoIds),
    };
    try {
      if (editing) {
        if (credential !== "") body.credential = credential;
        await api("PUT", "/providers/" + editing, body);
      } else {
        body.credential = credential;
        await api("POST", "/providers", body);
      }
      setEditing(null);
      setProtocol("");
      setBaseUrl("");
      setModels("");
      setHidden("");
      setTextOnly("");
      setImages("");
      setVideos("");
      setCredential("");
      setMessage(null);
      refresh();
    } catch (err) {
      report(setMessage, err);
    }
  }

  return (
    <>
      <form className="grid" onSubmit={save}>
        <label>协议<input value={protocol} placeholder="openai" onChange={(e) => setProtocol(e.target.value)} /></label>
        <label>Base URL<input value={baseUrl} placeholder="http://127.0.0.1:11434/v1" onChange={(e) => setBaseUrl(e.target.value)} /></label>
        <label>对话模型<input value={models} placeholder="用逗号分隔" onChange={(e) => setModels(e.target.value)} /></label>
        <label>隐藏模型<input value={hidden} placeholder="不出现在目录里，也不能调用" onChange={(e) => setHidden(e.target.value)} /></label>
        <label>不接受图片<input value={textOnly} placeholder="带图片或视频的请求会被拒绝" onChange={(e) => setTextOnly(e.target.value)} /></label>
        <label>图片模型<input value={images} placeholder="不进对话目录" onChange={(e) => setImages(e.target.value)} /></label>
        <label>视频模型<input value={videos} placeholder="不进对话目录" onChange={(e) => setVideos(e.target.value)} /></label>
        <label>密钥<input type="password" value={credential} placeholder={editing ? "留空则不更换密钥" : "上游密钥"} onChange={(e) => setCredential(e.target.value)} /></label>
        <button type="submit">{editing ? "更新提供方" : "新建提供方"}</button>
      </form>
      <Table headers={["ID", "协议", "Base URL", "模型", "已设置密钥", ""]} rows={list.map((p) => [
        p.id, p.protocol, p.baseUrl, (p.models || []).join(", "), p.credentialSet ? "是" : "否",
        <span className="row-actions" key={p.id}>
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
        : <Table headers={["开始", "模型", "调用方", "路由", "提供方", "结果", "耗时", "用量"]} rows={records.map((r) => [
          r.started, r.model || "", r.caller || r.tokenId || "", r.routeId, r.providerId, r.outcome, duration(r), r.usage ? JSON.stringify(r.usage) : "未知",
        ])} />}
    </>
  );
}
