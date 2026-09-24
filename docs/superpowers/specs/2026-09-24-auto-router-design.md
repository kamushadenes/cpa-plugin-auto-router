# cpa-plugin-auto-router — design

Data: 2026-09-24. Estado: rascunho para revisão do operador.

## Objetivo

Um plugin nativo do CLIProxyAPI que publica um modelo virtual, `auto-router`,
no catálogo do proxy (`/v1/models`). Um pedido para `auto-router` é
classificado por Jev (TypeSafe System One, via OpenRouter) em **categoria de
tarefa** e **dificuldade**; uma **tabela local de benchmarks** escolhe o
modelo concreto e o nível de thinking; o host executa o pedido no modelo
escolhido pelo caminho normal (credenciais, cooldown, retry, usage, log).

Referência de forma: <https://route.jev.works/> — opções fixas, uma pergunta
`choice`, pick + probabilidade por opção + confiança, threshold, fallback
seguro, histórico de decisões. Aqui as "opções" do Jev são rótulos de tarefa,
não modelos: a comparação entre modelos é determinística, local e auditável.

Fora de escopo nesta versão: Claude Code e Codex CLI como clientes (v1 atende
OMP e Hermes), classificador local (SemIf), persistência do estado de sessão,
UI no Management Center, aliases automáticos no updater.

## Decisões fechadas com o operador

| Tema | Decisão |
|---|---|
| Forma | Plugin nativo (`.so`, Go/cgo) com `model_registrar` + `model_router` + `executor` self. Não sidecar. |
| Clientes v1 | OMP (Responses) e Hermes (chat-completions). |
| O que sai do host para o Jev | Trecho da última mensagem do usuário (≤ 1.500 chars) + sinais locais. Nunca system prompt, tool results, anexos. |
| Re-roteamento | Reavalia a cada turno; **só escala**, nunca rebaixa. Sem escalada, mantém o modelo e o thinking gravados. |
| Desempate no tier | Maior score nos benchmarks da categoria. Custo só desempata empate real (dentro da margem publicada). |
| Jev | **Uma** chamada por turno com várias perguntas ortogonais. Sem passes encadeados. |
| Thinking | Mapeado direto da dificuldade (`low/high/xhigh/max`), igual para todo modelo do tier. O proxy clampa quando o modelo não suporta. |
| Visão | Não é categoria. Pedido com imagem filtra localmente os candidatos com `vision: true`. |
| Sem benchmark | Modelo sem score na categoria nunca vence; entra só como fallback. |
| Benchmarks | Escolhidos por categoria em pesquisa própria (dados baixados), só fontes legíveis por máquina sem chave. Scores são por (modelo, effort). |
| Atualização | Script + `systemd --user` timer semanal reescreve `models.yaml` filtrando pelo `/v1/models` do proxy. Tiers ficam em `tiers.yaml`, manual. |
| Políticas pessoais | **Nenhuma.** A decisão é 100% benchmark (Fable, Sonnet etc. concorrem como qualquer outro). Exclusões só por capacidade: modelos de imagem, aliases abliterated, `codex-auto-review`. |
| Repositório | Repo próprio (`cpa-plugin-auto-router`), fora do nix, como os plugins da store. |

## Fluxo por pedido

```
cliente → POST /v1/responses|/v1/chat/completions  model=auto-router
  │
  ├─ model.route (plugin)
  │    1. RequestedModel != auto-router → Handled:false (não é conosco)
  │    2. chave de sessão (mesma ordem do host: X-Session-ID, prompt_cache_key,
  │       X-Session-Affinity, hash da primeira mensagem)
  │    3. sessão já em `extremo` → decisão = gravada; pula o Jev
  │    4. body sem mensagem nova de usuário (só tool results) → decisão = gravada
  │    5. jev_decide(estado, perguntas)   ← 1 chamada, timeout curto
  │    6. escolha(tabela, categoria, dificuldade, tem_imagem, disponíveis)
  │    7. aplica regra de escalada contra o estado da sessão; grava
  │    8. log JSON da decisão; guarda decisão por request-id
  │    → Handled:true, TargetKind:self
  │
  └─ executor.execute / execute_stream (plugin)
       host.model.execute(_stream) { model: "<modelo>(<thinking>)",
                                     entry=exit=protocolo de entrada,
                                     host_callback_id }   ← host pula este roteador
       forward do stream chunk a chunk; header X-Auto-Router na resposta
```

O executor se declara com `executor_model_scope: static`,
`executor_input_formats: [responses, chat-completions]` e saída igual à
entrada. O `host.model.*` recebe `EntryProtocol == ExitProtocol == SourceFormat`
do pedido: o corpo passa **sem tradução** pelo plugin; a tradução para o
upstream é do host, como hoje.

## Classificação (Jev)

Estado enviado (`state`):

```json
{
  "context": "Pedido a um proxy de LLMs. Classifique a tarefa que o usuário está pedindo.",
  "item": "<últimos ≤1500 chars da última mensagem de usuário>",
  "signals": {"tools": 12, "images": 0, "messages": 37, "format": "responses"}
}
```

Perguntas (`questions`), todas na mesma chamada:

| nome | tipo | opções |
|---|---|---|
| `category` | choice | `webdev`, `backend`, `agentic-terminal`, `debugging`, `review`, `spec-design`, `writing`, `extraction`, `math-data` |
| `difficulty` | choice | `trivial`, `routine`, `hard`, `extreme` |

As probabilidades por opção que o `choice` devolve são gravadas no log; a v1
usa argmax. Mistura ponderada de benchmarks (60 % webdev / 40 % backend) fica
como upgrade se o argmax se mostrar instável em categorias vizinhas.

Confiança (`answers.<q>.confidence`) abaixo de **0,6**:

- `difficulty` incerta → `routine` em sessão nova; em sessão existente mantém
  o gravado. Dúvida nunca escala.
- `category` incerta → ranking geral (AA Intelligence), sem benchmark de
  categoria.

Jev indisponível, timeout ou resposta inválida → mesmo default, `reason:
jev-unavailable`, **o pedido segue**. O roteador é fail-open; nunca bloqueia.

Jev não é fronteira de segurança: texto injetado no prompt pode mover a
decisão. O pior caso é rotear para um modelo mais caro; nada destrutivo
depende do rótulo.

## Tabela de decisão

### dificuldade → tier + thinking

| dificuldade | tier | thinking |
|---|---|---|
| `trivial` | flash | `low` |
| `routine` | mid | `high` |
| `hard` | top | `xhigh` |
| `extreme` | top | `max` |

O thinking sai como sufixo no nome do modelo (`gpt-6-astra(xhigh)`). O host
(`internal/thinking/validate.go`) converte nível ↔ budget e clampa para o
nível mais próximo quando o modelo não suporta (`max` vira `high` em Kimi,
por exemplo); configuração vinda de sufixo é clampada, nunca rejeitada.

### categoria → benchmarks que pesam

Pesquisa de 2026-09-24 (dados baixados e lidos, não descrição de site).
Critério para entrar: mede a categoria de fato, publica margem de erro, tem
os modelos do catálogo, e é **legível por máquina sem chave** — senão o
updater (abaixo) não existe. SWE-bench Verified ficou de fora: última
entrada em fev/2026, nenhum modelo atual.

| categoria | benchmarks (ordem de peso) | por que |
|---|---|---|
| `webdev` | Arena WebDev; Arena text `coding` | preferência humana em apps web reais; texto-coding como reserva |
| `backend` | SWE-bench Pro v2 (Scale); SWE-Atlas Refactoring; Arena text `coding` | resolução de issue em repo real; refatoração; reserva |
| `agentic-terminal` | Terminal-Bench 4.0 (tbench.ai); Arena Agent (Code); AA Coding Agent Index | shell + muitos passos; orquestração de tools; composto |
| `debugging` | SWE-Atlas QnA; Terminal-Bench 4.0 | rastrear código e explicar comportamento; investigação em terminal |
| `review` | SWE-Atlas QnA; SWE-Atlas Test Writing; Arena text `coding` | ler e entender código alheio; cobrir com testes |
| `spec-design` | Arena text `hard_prompts`; AA Intelligence; GDPval-AA | raciocínio aberto e longo; trabalho profissional |
| `writing` | Arena text `creative_writing`; Arena text `instruction_following` | escrita; obedecer formato |
| `extraction` | nenhum — tier flash, vence o mais barato | tarefa não discrimina modelo |
| `math-data` | GPQA Diamond (AA); Arena text `math` | ciência/dados com gabarito; preferência em matemática |

Sem score em nenhum benchmark da lista para os candidatos do tier → Arena
text `overall` (cobre 19 dos 27 modelos, atualização semanal).

Fica de fora: Agent Security League (Endor; SecPass seria ótimo para
`review`, mas só HTML sem CI publicado), AIME/LiveCodeBench (poucos modelos
atuais no AA), Scale VLU/MultiChallenge (desatualizados), MMMU-Pro (visão é
filtro, não ranking).

### Scores são por (modelo, effort)

Todo benchmark acima publica por configuração de effort, não por modelo:
Terminal-Bench 4.0 lista Astra em `max/xhigh/high/medium/low` como cinco
linhas (58,2 / 57,9 / 57,9 / 54,2 / 50,6, CI ±3). A tabela guarda a chave
`modelo@effort`; a escolha compara os candidatos **no effort que vai usar**
(o da dificuldade). Se o modelo não tem linha nesse effort, usa a linha de
effort mais próximo abaixo — nunca uma acima (não pagar por score que não
vamos obter). Linhas sem effort declarado (Arena WebDev, Arena text sem
sufixo) valem para todo effort.

### Escolha

```
effort     = thinking(dificuldade)
candidatos = modelos da tabela com tier == tier(dificuldade)
           ∖ exclusões por capacidade
           ∩ (tem_imagem ? vision == true : todos)
           ∩ disponíveis no host (AvailableProviders / cooldown)
ranqueados = candidatos com score no primeiro benchmark da categoria em
             effort ≤ effort (o mais próximo)
           (nenhum candidato com score → tenta o próximo benchmark da lista)
vencedor   = maior score; empate (|Δ| ≤ max(margem publicada dos dois))
             → menor custo (input + output, models.dev)
sem ranqueado disponível → fallback: candidatos sem score, mais barato primeiro
sem candidato nenhum no tier → sobe um tier (nunca desce)
```

"Sem score" é diferente de "score zero": um modelo só entra no ranking se a
tabela tiver `{value, margin, date}` para aquele benchmark.

### Formato da tabela (`models.yaml`, ao lado do `.so`)

```yaml
generated_at: 2026-09-24T21:00:00Z          # escrito pelo updater
benchmarks:
  arena-webdev:      {source: "hf:lmarena-ai/leaderboard-dataset/webdev",            unit: elo}
  arena-coding:      {source: "hf:lmarena-ai/leaderboard-dataset/text_style_control#coding", unit: elo}
  terminal-bench-4:  {source: "https://www.tbench.ai/leaderboard/terminal-bench/4.0", unit: pct}
  swe-atlas-qna:     {source: "https://labs.scale.com/leaderboard/sweatlas-qna",     unit: pct}
  swe-bench-pro-v2:  {source: "https://labs.scale.com/leaderboard/swe_bench_pro_public_v2", unit: pct}
  gpqa-diamond:      {source: "https://artificialanalysis.ai/evaluations/gpqa-diamond", unit: pct}

models:
  gpt-6-astra:
    tier: top
    vision: true                             # models.dev
    cost: {input: 10, output: 50}            # USD / M tokens, models.dev
    scores:
      arena-webdev:
        - {effort: max,  value: 1792.2, margin: 12.1, date: 2026-09-23}
      terminal-bench-4:
        - {effort: max,   value: 58.18, margin: 2.79, date: 2026-09-03}
        - {effort: xhigh, value: 57.88, margin: 2.7,  date: 2026-09-03}
        - {effort: high,  value: 57.88, margin: 3.0,  date: 2026-09-03}
        - {effort: low,   value: 50.6,  margin: 2.8,  date: 2026-09-03}
      swe-atlas-qna:
        - {effort: xhigh, value: 59.14, margin: 4.88, date: 2026-09-09}
  gpt-5.6-luna:
    tier: mid
    vision: true
    cost: {input: 0.2, output: 1.2}
    scores:
      arena-webdev:
        - {effort: null, value: 1519, margin: 8, date: 2026-09-23}
      terminal-bench-4:
        - {effort: max, value: 17.3, margin: 2.9, date: 2026-06-26}
  mimo-v2.6-flash:
    tier: flash
    vision: true
    cost: {input: 0, output: 0}
    scores: {}                               # sem benchmark → só fallback
```

Regras de carga: score sem `date`/`margin`, benchmark não declarado ou tier
inválido rejeita o arquivo inteiro (o plugin segue com a tabela anterior e
loga erro). Recarga por `mtime` a cada decisão (um `stat`), sem watcher. O
plugin nunca consulta leaderboards: quem escreve o YAML é o updater.

O `tier` de cada modelo é declarado num arquivo separado que o updater não
toca (`tiers.yaml`): flash = `glm-5.3-flash`, `deepseek-v4-flash`,
`qwen-3.8-flash-next`, `mimo-v2.6-flash`; mid = `gpt-5.6-luna`, `gpt-6-luna`,
`gpt-5.6-terra`, `glm-5.3`, `kimi-k3`, `claude-sonnet-5`, `deepseek-v4-pro`,
`qwen-3.8-max`, `minimax-m3`; top = `gpt-6-astra`, `gpt-6-sol`, `gpt-5.6-sol`,
`claude-opus-5`, `claude-opus-5-5`, `claude-fable-5`, `claude-fable-5-1`.
Modelo do catálogo sem tier declarado é ignorado com aviso no log do updater.

## Updater de benchmarks (systemd timer)

Um script Python (stdlib + `pyarrow` para o parquet do Arena) roda num
`systemd --user` timer semanal no host do proxy, com `Persistent=true`, e
reescreve `models.yaml` atomicamente (escreve `.tmp`, valida, `rename`).

```
1. catálogo   = GET http://127.0.0.1:8317/v1/models  (chave do proxy)
              ∩ tiers.yaml                          ← só o que temos E tem tier
2. capacidade = models.dev api.json → vision, custo por modelo
3. por fonte:
   arena     parquet `latest` de text_style_control e webdev (HF, sem chave)
             → rating, rating_lower/upper, leaderboard_publish_date
   tbench    HTML → payload RSC → "rows"[].metrics.accuracy, ci95_half_width,
             metadata.reasoning_effort, date
   scale     HTML → payload RSC → "entries"[].score, confidenceInterval_upper,
             createdAt; effort vem do nome ("xHigh", "max")
   aa        HTML → <script type="application/ld+json"> Dataset → data[]
             (top-20; effort entre parênteses no label; sem CI → margin fixa
             por benchmark, declarada no script)
4. aliases: mapa explícito nome-na-fonte → id do catálogo, por fonte,
   mantido à mão no script (ex.: "GPT 6 Astra (Codex) xHigh*" →
   gpt-6-astra@xhigh). Nome novo sem alias → aviso no log, linha ignorada.
   Nunca casar por substring solta.
5. só substitui um score se a nova data ≥ a gravada; fonte fora do ar mantém
   os scores anteriores dela (a tabela nunca regride a vazio).
6. valida com as mesmas regras do loader do plugin; falha → não toca no
   arquivo, exit 1, log.
```

Detalhes verificados em 2026-09-24:

- Arena: `https://huggingface.co/datasets/lmarena-ai/leaderboard-dataset/resolve/refs%2Fconvert%2Fparquet/<config>/latest/0000.parquet`
  (text_style_control 590 KB, webdev 27 KB, agent ~8 KB). A API `/rows` do
  HF dá 429 em uso contínuo; o parquet não.
- tbench.ai e Scale são Next.js RSC: os dados vêm em `self.__next_f.push`
  como JSON escapado; extrair `"rows"` / `"entries"` por casamento de
  colchetes. Frágil por natureza — quebra de layout = fonte pulada com aviso,
  scores antigos ficam.
- AA: ld+json é estável e público; a Data API (`/api/v2/language/models/free`,
  chave grátis, 100 req/dia) só dá índices compostos — não substitui as
  páginas por avaliação.
- AA "with fallback" nos Claude é o modo que a AA rodou; tratar como o
  effort declarado, anotar `note: with-fallback`.
- `ponytail:` o mapa de aliases é manual; automatizar só se a manutenção
  virar rotina.

Unidade: `systemd --user` do `hermes` (linger ativo), `OnCalendar=weekly`,
`RandomizedDelaySec=1h`, `Persistent=true`; o serviço roda
`cpa-auto-router-update --catalog http://127.0.0.1:8317 --out <dir>/models.yaml`.
A chave do proxy vem de `EnvironmentFile` já existente. Uma run manual
(`systemctl --user start`) é o teste; `journalctl --user -u` mostra o
resumo: modelos cobertos por fonte, linhas ignoradas, scores atualizados.

## Sessão e escalada

Estado por sessão, em memória: `{difficulty, model, thinking, updated_at}`.
TTL 1 h (mesmo `session-affinity-ttl` do proxy), teto de 65 536 entradas com
descarte do mais antigo. Perde no restart: uma sessão viva é reclassificada do
zero uma vez. `ponytail:` persistir só se isso incomodar na prática.

Regra por turno:

| situação | ação | `reason` |
|---|---|---|
| sessão nova | classifica, escolhe, grava | `new` |
| dificuldade ≤ gravada | mantém modelo e thinking | `keep` |
| escalou, mesmo tier | mantém modelo, sobe thinking | `escalate-thinking` |
| escalou, tier acima | escolhe de novo com a categoria deste turno | `escalate-tier` |
| imagem e o modelo gravado não tem visão | melhor modelo **com visão do mesmo tier**, mesma categoria e thinking | `vision-swap` |
| já em `extreme` | não chama o Jev | `keep` |
| Jev falhou | default acima | `jev-unavailable` |
| modelo gravado indisponível | escolhe de novo no mesmo tier | `fallback` |

Mudança de categoria no meio da sessão **não** troca modelo por si só. Manter
o modelo preserva o cache de prompt, que domina o custo real.

## Observabilidade

- Uma linha JSON por decisão no log do proxy: `session` (hash curto),
  `category` + probabilidade, `difficulty` + probabilidade, `confidence`,
  `tier`, `model`, `thinking`, `reason`, `jev_ms`. Equivale ao "routing
  history" do route.jev.works.
- Header de resposta `X-Auto-Router: <model>(<thinking>);<reason>` via
  `ExecutorResponse.Headers`, para ver no cliente quem respondeu sem abrir log.
- `plugin.register` expõe `ConfigFields`: `enabled`, `jev_api_key_env`,
  `jev_base_url`, `jev_model`, `confidence_threshold` (0,6), `table_path`,
  `snippet_chars` (1500), `jev_timeout` (2 s).

## Segurança e privacidade

- Chave do Jev lida de variável de ambiente nomeada na config, nunca do YAML.
- URL do Jev: https sempre; http só loopback/LAN (mesma política do plugin
  `jev` do Hermes).
- Sai do host apenas o trecho da última mensagem do usuário e contadores. Sem
  system prompt, sem tool results, sem imagens, sem headers.
- O plugin não vê nem guarda credenciais de upstream: usa `host.model.*`.
- Falha do roteador nunca bloqueia: default + log.

## Testes

- Núcleo de decisão (`decide(tabela, resposta_jev, estado, sinais)`) é puro e
  testado com casos gravados: empate por custo, sem score → fallback, escalada
  só sobe, `vision-swap`, categoria incerta, Jev fora, subida de tier quando o
  tier está vazio, score no effort mais próximo abaixo (nunca acima).
- Loader da tabela: rejeita score sem data/margem, benchmark não declarado,
  tier inválido.
- Updater: um teste por fonte com HTML/parquet gravado em `testdata/` (o
  parser quebra visível quando o site mudar, não em produção); teste de que
  fonte fora do ar preserva os scores anteriores.
- Smoke real contra o proxy: `curl` para `auto-router` com um pedido trivial e
  um difícil; conferir `X-Auto-Router` e a linha de log.

## Verificações antes de codar

Empíricas; nenhuma muda o desenho, só confirma o caminho:

1. Compilar e carregar o exemplo oficial `examples/plugin/claude-web-search-router`
   no binário 7.2.159 (ABI 1, schema 6) com Go 1.27.1 do host.
2. Sufixo `modelo(nivel)` via `host.model.execute` cai em `ValidateConfig` com
   `fromSuffix=true` (caminho clamp, não erro).
3. Qual sinal de sessão OMP e Hermes mandam de fato (`prompt_cache_key`?
   header?); confirmar que o hash da primeira mensagem estabiliza quando não há.
4. `ExecutorResponse.Headers` chega ao cliente em stream e não-stream.
5. `host.model.execute_stream` com `entry == exit == responses` devolve o SSE
   de Responses intacto (reasoning items, tool calls).

## Fontes

- Seam do roteador: `sdk/api/handlers/handlers_routing.go` (`applyModelRouter`,
  `providersForExecution`), `internal/pluginhost/model_router.go`.
- Contrato: `sdk/pluginapi/types.go` (`ModelRouteRequest/Response`,
  `ExecutorResponse`, `HostModelExecutionRequest`), `sdk/pluginabi/types.go`.
- Thinking: `internal/thinking/suffix.go`, `internal/thinking/validate.go`.
- Sessão: `sdk/cliproxy/auth/selector.go` (`ExtractSessionID`, ordem de sinais).
- Exemplo com forward de stream: `examples/plugin/claude-web-search-router/go/`.
- Docs: <https://help.router-for.me/plugin/model-router>,
  `/plugin/model-registrar`, `/plugin/executor`, `/plugin/host-callbacks`.
- Jev: plugin `jev` do Hermes (`client.py`, política de URL e forma do wire).
- Benchmarks (dados lidos em 2026-09-24): Arena
  `hf:lmarena-ai/leaderboard-dataset` (parquet `latest`, publish 2026-09-13 /
  webdev 2026-09-23); tbench.ai Terminal-Bench 4.0 e 2.1 (payload RSC);
  Scale `sweatlas-qna`, `sweatlas-tw`, `sweatlas-refactoring`,
  `swe_bench_pro_public_v2`, `mcp_atlas` (payload RSC); AA
  `/evaluations/{gpqa-diamond, artificial-analysis-intelligence-index,
  gdpval-aa, terminalbench-4-0}` (ld+json); models.dev `api.json` (visão,
  custo, níveis de effort por modelo).
- Cobertura medida sobre os 27 modelos do catálogo com tier: Arena text 19,
  Arena WebDev 23, Arena Agent 19, Terminal-Bench 4.0 9, SWE-Atlas QnA 8,
  SWE-bench Pro v2 8, AA por avaliação 10–11.
