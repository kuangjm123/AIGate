import { useEffect, useState } from "react";
import { api, asList } from "../api.js";

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
      }).catch(() => {});
  }, [tick]);

  const gateway = listeners && listeners.gateway;
  const base = gateway && gateway.effective ? "http://" + gateway.effective : "";
  return <>
    <p className="section-intro">需要手动配置应用时，将下面三项填入应用的模型服务设置。本页不会修改任何配置文件。</p>
    <div className="connect-grid">
      <article className="connect-card"><span className="step">01</span><h3>基地址</h3><p>请求发送到的网关地址</p><code>{base || "网关未监听"}</code></article>
      <article className="connect-card"><span className="step">02</span><h3>密钥</h3><p>在「网关令牌」页面创建，完整密钥只显示一次</p><code>Bearer · gateway token</code></article>
      <article className="connect-card"><span className="step">03</span><h3>模型</h3><p>选择下方任一可用模型名</p><div className="model-list">{models.length ? models.map((model) => <code key={model}>{model}</code>) : <span>暂无可用模型，请先添加提供方或路由</span>}</div></article>
    </div>
  </>;
}
