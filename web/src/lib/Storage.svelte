<script>
  import Kpi from './Kpi.svelte';
  import Icon from './icons/Icon.svelte';
  import { snapshot, systemError, adapters, adapterFor } from './data.js';
  import { now } from './ticker.js';
  import { connectDocs } from './urls.js';
  import { formatBytes, formatRate, formatPercent, formatAge } from './format.js';
  import { serviceStatus } from './status.js';
  import { diskParts, diskTotals, mountServices, mountSummary, mountProblem, percentOf } from './storage.js';

  // Storage shows the host's disks and, for every service that reports mounts, each mount with its cache and
  // the transfers in progress. services is every known service; only those declaring the "mounts" shape show.
  let { services = [] } = $props();

  const TOP_TRANSFERS = 5;

  const disks = $derived($snapshot?.host?.disks ?? []);
  const totals = $derived(diskTotals(disks));
  const sources = $derived(mountServices(services));
  const sum = $derived(mountSummary(sources));
  // The apps that can report mounts, by name, as the adapters list gives them.
  const mountApps = $derived($adapters.filter((a) => a.kinds?.includes('mounts')));

  // cacheText is the figures of a mount that keeps a disk cache.
  function cacheText(m) {
    return m.cacheMaxBytes > 0 ?
      `${formatBytes(m.cacheUsedBytes)} of ${formatBytes(m.cacheMaxBytes)}` : `${formatBytes(m.cacheUsedBytes)}, no limit`;
  }

  function stateChip(m) {
    if (m.state === 'mounted') return { text: 'mounted', tone: 'ok', title: `mounted at ${m.mountPoint}` };
    if (m.state === 'unmounted') return { text: 'not mounted', tone: 'fail', title: 'not mounted' };
    return {
      text: 'in use', tone: '',
      title: m.unlisted ?
        'The remote is served but not mounted through the remote control; whether it is mounted otherwise is not reported' :
        'The remote is served, but where or whether it is mounted is not reported',
    };
  }

  function plural(n, word) {
    return `${n} ${word}${n === 1 ? '' : 's'}`;
  }

  const mountsTone = $derived(sum.readings === 0 ? '' : sum.healthy < sum.listed ? 'warn' : 'ok');
  const mountsSub = $derived(
    [sum.healthy < sum.listed ? `${plural(sum.listed - sum.healthy, 'mount')} need a look` : 'All healthy',
      sum.total > sum.listed ? `+${sum.total - sum.listed} not listed` : ''].filter(Boolean).join(' · '),
  );
  const cacheSub = $derived(
    sum.caches === 0 ? 'No disk cache' :
      sum.cacheMax > 0 ? `of ${formatBytes(sum.cacheMax)} · ${percentOf(sum.cacheUsed, sum.cacheMax)}%` :
        `${plural(sum.caches, 'cache')}, not all limited`,
  );
</script>

<div class="storage">
  {#if $systemError && !$snapshot}
    <p class="error" role="alert">{$systemError}</p>
  {/if}

  <div class="kpis">
    {#if disks.length > 0}
      <Kpi icon="hard-drive" label="Disks used" value={formatBytes(totals.used)}
        sub={`${formatBytes(totals.avail)} free · ${totals.pct}%`} bar={totals.pct}
        barTone={totals.pct >= 90 ? 'warn' : ''} />
    {:else}
      <Kpi icon="hard-drive" label="Disks used" value="—" sub="No disks measured" />
    {/if}
    {#if sum.readings > 0}
      <Kpi icon="folder-sync" label="Mounts healthy" value={`${sum.healthy}/${sum.listed}`} tone={mountsTone}
        sub={mountsSub} />
      <Kpi icon="database" label="Cache used" value={sum.caches ? formatBytes(sum.cacheUsed) : '—'} sub={cacheSub}
        bar={sum.cacheMax > 0 ? percentOf(sum.cacheUsed, sum.cacheMax) : null}
        barTone={sum.cacheMax > 0 && percentOf(sum.cacheUsed, sum.cacheMax) >= 90 ? 'warn' : ''} />
      <Kpi icon="arrow-down-up" label="Transfer speed" value={formatRate(sum.speed)}
        sub={sum.active ? `${plural(sum.active, 'transfer')} in progress` : 'Nothing transferring'} />
    {:else}
      <Kpi icon="folder-sync" label="Mounts healthy" value="—" sub={sources.length ? 'Waiting for a reading' : 'No mount service'} />
      <Kpi icon="database" label="Cache used" value="—" sub="No reading" />
      <Kpi icon="arrow-down-up" label="Transfer speed" value="—" sub="No reading" />
    {/if}
  </div>

  {#if disks.length > 0}
    <section class="card" aria-labelledby="storage-disks-title">
      <div class="card-head">
        <h2 id="storage-disks-title"><Icon name="hard-drive" size={16} />Disks</h2>
        <span class="meta num" title={`${formatBytes(totals.used)} used of ${formatBytes(totals.total)}`}>
          {formatBytes(totals.total)} in {plural(totals.count, 'file system')}
        </span>
      </div>
      <div class="disks">
        {#each disks as d (d.path)}
          {@const p = diskParts(d)}
          <div class="disk">
            <div class="disk-head">
              <span class="ellipsis path" title={d.path}>{d.path}</span>
              <span class="muted num figures" title={`${formatBytes(p.used)} used, ${formatBytes(p.reserved)} reserved, ${formatBytes(p.avail)} free of ${formatBytes(d.total)}`}>
                {formatBytes(p.used)} used · <span class="free">{formatBytes(p.avail)} free</span> · {p.pct}%
              </span>
            </div>
            <div class="stack" role="img"
              aria-label={`${d.path}: ${formatBytes(p.used)} used, ${formatBytes(p.avail)} free of ${formatBytes(d.total)}, ${p.pct}% full`}>
              <i class="used" class:warn={p.pct >= 90} style:flex-grow={p.used}></i>
              {#if p.reserved > 0}<i class="reserved" style:flex-grow={p.reserved}></i>{/if}
              <i class="avail" style:flex-grow={p.avail}></i>
            </div>
          </div>
        {/each}
      </div>
      <p class="legend">
        <span><i class="sw used"></i>Used</span>
        {#if disks.some((d) => d.free > d.avail)}<span><i class="sw reserved"></i>Reserved</span>{/if}
        <span><i class="sw avail"></i>Free</span>
      </p>
    </section>
  {/if}

  {#if sources.length === 0}
    <div class="empty">
      <Icon name="cloud" size={28} />
      <b>No mounts monitored yet</b>
      {#if mountApps.length}
        <span>
          Add {mountApps.map((a) => a.name).join(' or ')} on the Apps view to see its mounts, caches and uploads.
          Observarr only makes read-only calls to it.
        </span>
        <span class="links">
          {#each mountApps as a (a.type)}
            <a href={connectDocs(a.name)} target="_blank" rel="noopener noreferrer">How to connect {a.name}</a>
          {/each}
          <a href="#/apps">Go to Apps</a>
        </span>
      {:else}
        <span>Connect a service that mounts remote storage to see its mounts and caches.</span>
      {/if}
    </div>
  {/if}

  {#each sources as svc (svc.id)}
    {@const reading = svc.readings?.mounts}
    {@const data = reading?.data}
    {@const adapter = adapterFor($adapters, svc.type)}
    {@const transfers = (data?.transfers ?? []).slice(0, TOP_TRANSFERS)}
    <section class="card" aria-labelledby={`mounts-title-${svc.id}`}>
      <div class="card-head">
        <h2 id={`mounts-title-${svc.id}`}>
          <span class="svc-icon" aria-hidden="true">
            <Icon name={adapter?.icon} size={16} /><span class="status-dot {serviceStatus(svc)}"></span>
          </span>
          <span class="ellipsis" title={`Mounts & caches · ${svc.name}`}>Mounts &amp; caches · {svc.name}</span>
        </h2>
        {#if reading}<span class="meta" title={reading.at}>{formatAge(reading.at, $now)}</span>{/if}
      </div>

      {#if !reading}
        <p class="waiting">Waiting for the first check…</p>
      {:else if reading.error}
        <p class="error" role="alert">{reading.error}</p>
      {:else if data}
        {#if data.mounts.length === 0}
          <p class="waiting">No mounts reported.</p>
        {:else}
          <ul class="mount-list">
            {#each data.mounts as m, i (`${m.remote}|${m.mountPoint ?? ''}|${i}`)}
              {@const st = stateChip(m)}
              {@const problem = mountProblem(m)}
              {@const pct = m.cache && m.cacheMaxBytes > 0 ? percentOf(m.cacheUsedBytes, m.cacheMaxBytes) : null}
              <li>
                <div class="m-head">
                  <span class="dot {problem ? problem.tone : st.tone || 'unknown'}" aria-hidden="true"></span>
                  {#if m.mountPoint}
                    <span class="route ellipsis" title={`${m.remote} → ${m.mountPoint}`}>
                      <span class="remote">{m.remote}</span>
                      <span class="muted" aria-hidden="true">→</span>
                      <span class="visually-hidden">mounted at</span>
                      <span class="point">{m.mountPoint}</span>
                    </span>
                  {:else}
                    <span class="route ellipsis" title={`${m.remote} · mount point unknown`}>
                      <span class="remote">{m.remote}</span>
                    </span>
                  {/if}
                  <span class="chip dotted {st.tone}" title={st.title}>{st.text}</span>
                </div>
                <div class="m-body">
                  <div class="m-cache">
                    {#if pct !== null}
                      <div class="bar" class:warn={pct >= 90} class:fail={m.outOfSpace} role="img"
                        aria-label={`Cache ${cacheText(m)}, ${pct}%`}>
                        <i style:width={`${m.outOfSpace ? 100 : pct}%`}></i>
                      </div>
                    {/if}
                    {#if m.cache}
                      <span class="num cache-text ellipsis" title={`Cache ${cacheText(m)}`}>
                        <span class="muted">Cache</span> {cacheText(m)}
                      </span>
                    {:else if m.cacheMode}
                      <span class="cache-text muted" title={`Cache mode ${m.cacheMode}: nothing is cached on disk`}>No disk cache</span>
                    {:else}
                      <span class="cache-text muted" title="The app does not report a cache mode">Cache unknown</span>
                    {/if}
                  </div>
                  <div class="chips">
                    {#if m.outOfSpace}<span class="chip fail" title="The cache disk is out of space">out of space</span>{/if}
                    {#if m.erroredFiles > 0}
                      <span class="chip fail num" title={`${m.erroredFiles} cached files could not be uploaded`}>{m.erroredFiles} errored</span>
                    {/if}
                    {#if m.uploadsInProgress > 0}
                      <span class="chip ok num" title={`${m.uploadsInProgress} uploads in progress`}>↑ {m.uploadsInProgress} uploading</span>
                    {/if}
                    {#if m.uploadsQueued > 0}
                      <span class="chip warn num" title={`${m.uploadsQueued} uploads queued`}>{m.uploadsQueued} queued</span>
                    {/if}
                    {#if m.cache && m.cacheMode}<span class="chip" title={`cache mode ${m.cacheMode}`}>{m.cacheMode}</span>{/if}
                    {#if m.cache}
                      <span class="chip num" title={`${m.cachedFiles} files in the cache`}>{plural(m.cachedFiles, 'file')}</span>
                    {/if}
                  </div>
                </div>
              </li>
            {/each}
          </ul>
          {#if data.total > data.mounts.length}
            <p class="muted more">and {data.total - data.mounts.length} more</p>
          {/if}
        {/if}

        <div class="transfers">
          <p class="t-head">
            <span class="label">Transfers</span>
            <span class="num t-figures" title={`${formatRate(data.speed)}, ${data.activeTransfers} in progress, ${formatBytes(data.bytes)} since the app started`}>
              {formatRate(data.speed)} · {data.activeTransfers} active
            </span>
            {#if data.errors > 0}
              <span class="chip fail num" title={`${data.errors} transfer errors since the app started`}>{plural(data.errors, 'error')}</span>
            {/if}
          </p>
          {#if transfers.length === 0}
            <p class="waiting">Nothing transferring.</p>
          {:else}
            <ul class="t-list">
              {#each transfers as tr, i (`${tr.name}|${i}`)}
                <li>
                  <span class="ellipsis t-name" title={tr.name}>{tr.name}</span>
                  <span class="num t-pct" title={`${formatPercent(tr.progress)} of ${formatBytes(tr.sizeBytes)} at ${formatRate(tr.speed)}`}>
                    {formatPercent(tr.progress)}
                  </span>
                  <div class="bar t-bar" role="progressbar" aria-valuemin="0" aria-valuemax="100"
                    aria-valuenow={Math.round(tr.progress * 100)} aria-label={`${tr.name}: ${formatPercent(tr.progress)} done`}>
                    <i style:width={formatPercent(tr.progress)}></i>
                  </div>
                </li>
              {/each}
            </ul>
            {#if data.activeTransfers > transfers.length}
              <p class="muted more">and {data.activeTransfers - transfers.length} more</p>
            {/if}
          {/if}
        </div>
      {/if}
    </section>
  {/each}
</div>

<style>
  .storage { display: grid; gap: 16px; }

  .disks { display: grid; gap: 14px; }
  .disk { min-width: 0; }
  .disk-head { display: flex; justify-content: space-between; align-items: baseline; gap: 12px; margin-bottom: 8px; min-width: 0; font-size: 13px; }
  .path { font-weight: 550; flex: none; max-width: 60%; }
  .figures { flex: 0 1 auto; min-width: 0; font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .free { color: var(--ok); }
  .stack { display: flex; gap: 2px; height: 14px; border-radius: 7px; overflow: hidden; background: var(--surface3); }
  .stack > i { display: block; flex-basis: 0; min-width: 2px; transition: flex-grow 200ms var(--ease); }
  .used { background: var(--accent); }
  .used.warn { background: var(--warn); }
  .reserved { background: var(--dim); }
  .avail { background: var(--surface3); }
  .legend { display: flex; gap: 16px; margin-top: 14px; font-size: 12px; color: var(--muted); flex-wrap: wrap; }
  .legend span { display: inline-flex; align-items: center; gap: 6px; }
  .sw.avail { box-shadow: inset 0 0 0 1px var(--border2); }


  .svc-icon { position: relative; display: inline-grid; color: var(--muted); flex: none; }
  .svc-icon .status-dot { width: 9px; height: 9px; right: -4px; bottom: -3px; border-width: 2px; border-color: var(--surface); }
  .card-head h2 { min-width: 0; }

  .mount-list { list-style: none; margin: 0; padding: 0; display: grid; }
  .mount-list li { display: grid; gap: 8px; padding: 12px 0; border-bottom: 1px solid var(--border); min-width: 0; }
  .mount-list li:first-child { padding-top: 0; }
  .m-head { display: flex; align-items: center; gap: 9px; min-width: 0; font-size: 13.5px; }
  .route { flex: 1 1 auto; min-width: 0; }
  .remote { font-weight: 600; }
  .point { font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 12.5px; }
  .m-head .chip { flex: none; }
  .m-body {
    display: grid; grid-template-columns: minmax(0, 1fr) minmax(0, 340px); grid-template-areas: 'chips cache';
    align-items: center; gap: 8px 20px; padding-left: 16px;
  }
  .m-cache { grid-area: cache; display: flex; align-items: center; justify-content: flex-end; gap: 12px; min-width: 0; }
  .m-cache .bar { flex: 1 1 auto; min-width: 48px; }
  .cache-text { flex: 0 1 auto; min-width: 0; font-size: 12.5px; color: var(--text2); }
  .chips { grid-area: chips; display: flex; flex-wrap: wrap; gap: 6px; min-width: 0; }
  .chips:empty { display: none; }
  .chips .chip { flex: 0 1 auto; min-width: 0; }
  .more { font-size: 12.5px; padding-top: 8px; }

  .transfers { display: grid; gap: 8px; padding-top: 14px; }
  .t-head { display: flex; align-items: center; gap: 10px; min-width: 0; }
  .label { font-size: 13px; font-weight: 600; flex: none; }
  .t-figures { font-size: 12.5px; color: var(--text2); white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0; }
  .t-head .chip { margin-left: auto; flex: none; }
  .t-list { list-style: none; margin: 0; padding: 0; display: grid; gap: 9px; }
  .t-list li { display: grid; grid-template-columns: minmax(0, 1fr) auto; gap: 4px 12px; align-items: baseline; min-width: 0; }
  .t-name { font-size: 13px; }
  .t-pct { font-size: 12.5px; color: var(--text2); }
  .t-bar { grid-column: 1 / -1; }

  @container (max-width: 640px) {
    .m-body { grid-template-columns: minmax(0, 1fr); grid-template-areas: 'cache' 'chips'; padding-left: 0; }
    .m-cache { justify-content: flex-start; }
  }
</style>
