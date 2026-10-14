// SPDX-License-Identifier: Apache-2.0
//
// Virtualized event stream (SPEC §16.3, DESIGN §5.9): timestamp · level chip · service · shown
// columns (properties or built-ins) · rendered message with template values highlighted; infinite
// scroll by cursor; inline detail below the selected row; 2-line cards on phones (DESIGN §9.12).

import { useVirtualizer } from '@tanstack/react-virtual';
import { ArrowUp, X } from 'lucide-react';
import { useEffect, useRef, type ReactNode } from 'react';
import type { LogEvent } from '../../api/types';
import { Button, Kbd, LevelChip, Spinner } from '../../components/controls';
import { cx } from '../../lib/cx';
import { messageSegments } from '../../lib/template';
import { formatClock, formatDateTime } from '../../lib/time';
import { columnValue, rowMinWidth, rowTemplate } from './columns';
import s from './EventList.module.css';

export type Density = 'compact' | 'comfortable';

const ROW_HEIGHT: Record<Density, number> = { compact: 28, comfortable: 36 };
const CARD_HEIGHT = 84;
const LOAD_AHEAD = 30;

export function Message({ ev }: { ev: LogEvent }) {
  const segs = messageSegments(ev.message, ev.messageTemplate, ev.properties);
  return (
    <>
      {segs.map((seg, i) =>
        seg.kind === 'text' ? (
          seg.text
        ) : (
          <span key={i} className={s.pv}>
            {seg.text}
          </span>
        ),
      )}
    </>
  );
}

export interface EventListProps {
  events: readonly LogEvent[];
  selectedId: string | null;
  cursor: number;
  onCursor: (i: number) => void;
  onToggle: (ev: LogEvent) => void;
  density: Density;
  pinned: readonly string[];
  /** Removes a shown column (the × in its header). */
  onHideColumn: (field: string) => void;
  showDate: boolean;
  hasMore: boolean;
  loadingMore: boolean;
  onLoadMore: () => void;
  live: boolean;
  newCount: number;
  /** Called when the list scrolls to (true) or away from (false) the newest events. */
  onTopChange: (atTop: boolean) => void;
  onJumpTop: () => void;
  renderDetail: (ev: LogEvent) => ReactNode;
  mobile: boolean;
  /** Mobile: the card whose action row is open. */
  actionsFor: string | null;
  renderActions: (ev: LogEvent) => ReactNode;
}

export function EventList(props: EventListProps) {
  const {
    events,
    selectedId,
    cursor,
    density,
    pinned,
    showDate,
    hasMore,
    loadingMore,
    onLoadMore,
    live,
    newCount,
    mobile,
    actionsFor,
  } = props;
  const scroller = useRef<HTMLDivElement>(null);
  const estimate = mobile ? CARD_HEIGHT : ROW_HEIGHT[density];
  const virt = useVirtualizer({
    count: events.length + 1,
    getScrollElement: () => scroller.current,
    estimateSize: (i) => (i < events.length && events[i]?.id === selectedId ? estimate + 420 : estimate),
    overscan: 12,
    getItemKey: (i) => events[i]?.id ?? 'footer',
  });
  const items = virt.getVirtualItems();
  const last = items[items.length - 1];

  useEffect(() => {
    if (last !== undefined && last.index >= events.length - LOAD_AHEAD && hasMore && !loadingMore)
      onLoadMore();
  }, [last, events.length, hasMore, loadingMore, onLoadMore]);

  const cursorRef = useRef(cursor);
  useEffect(() => {
    if (cursor !== cursorRef.current && cursor >= 0) virt.scrollToIndex(cursor, { align: 'auto' });
    cursorRef.current = cursor;
  }, [cursor, virt]);

  const template = rowTemplate(pinned.length);
  // Below this width the whole list (header and rows together) scrolls sideways.
  const minWidth = mobile || pinned.length === 0 ? undefined : rowMinWidth(pinned.length);
  return (
    <div className={cx(s.list, mobile && s.mobile, density === 'comfortable' && s.comfortable)}>
      {!mobile ? (
        <div className={s.header} style={{ gridTemplateColumns: template, minWidth }} role="row">
          <span role="columnheader">Timestamp</span>
          <span role="columnheader">Level</span>
          <span role="columnheader">Service</span>
          {pinned.map((p) => (
            <span key={p} role="columnheader" className={s.pinnedHead}>
              <span title={p}>{p}</span>
              <button
                type="button"
                className={s.hideColumn}
                aria-label={`Remove column ${p}`}
                title="Remove column"
                onClick={() => props.onHideColumn(p)}
              >
                <X size={12} aria-hidden="true" />
              </button>
            </span>
          ))}
          <span role="columnheader">Message</span>
        </div>
      ) : null}
      {live && newCount > 0 ? (
        <button type="button" className={s.newPill} onClick={props.onJumpTop}>
          <ArrowUp size={14} aria-hidden="true" /> {newCount} new events
        </button>
      ) : null}
      <div
        ref={scroller}
        className={s.scroller}
        style={{ minWidth }}
        role="grid"
        aria-label="Events"
        aria-rowcount={events.length}
        onScroll={(e) => {
          props.onTopChange(e.currentTarget.scrollTop < 4);
        }}
      >
        <div className={s.inner} style={{ height: virt.getTotalSize() }}>
          {items.map((it) => {
            const ev = events[it.index];
            if (ev === undefined) {
              return (
                <div
                  key={it.key}
                  data-index={it.index}
                  ref={virt.measureElement}
                  className={s.footer}
                  style={{ transform: `translateY(${it.start}px)` }}
                >
                  {loadingMore ? (
                    <Spinner size={14} />
                  ) : hasMore ? (
                    <Button size="sm" variant="ghost" onClick={onLoadMore}>
                      Load more
                    </Button>
                  ) : live ? (
                    <span className={s.liveNote}>
                      <span className={s.liveDot} aria-hidden="true" /> Live · tailing newest events ·{' '}
                      <Kbd>j</Kbd> <Kbd>k</Kbd> move · <Kbd>↵</Kbd> expand
                    </span>
                  ) : events.length > 0 ? (
                    <span className={s.liveNote}>
                      End of results · <Kbd>j</Kbd> <Kbd>k</Kbd> move · <Kbd>↵</Kbd> expand
                    </span>
                  ) : null}
                </div>
              );
            }
            const selected = ev.id === selectedId;
            const t = Date.parse(ev.timestamp);
            return (
              <div
                key={it.key}
                data-index={it.index}
                ref={virt.measureElement}
                className={s.item}
                style={{ transform: `translateY(${it.start}px)` }}
              >
                <div
                  role="row"
                  aria-selected={selected}
                  aria-rowindex={it.index + 1}
                  tabIndex={it.index === cursor ? 0 : -1}
                  className={cx(
                    mobile ? s.card : s.row,
                    selected && s.selected,
                    it.index === cursor && s.cursor,
                  )}
                  style={mobile ? undefined : { gridTemplateColumns: template }}
                  onClick={() => {
                    props.onCursor(it.index);
                    props.onToggle(ev);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter' && e.target === e.currentTarget) props.onToggle(ev);
                  }}
                >
                  {mobile ? (
                    <>
                      <div className={s.cardHead}>
                        <LevelChip level={ev.level} />
                        <span className={s.service}>{ev.service}</span>
                        <span className={s.cardTime}>{formatClock(t)}</span>
                      </div>
                      <div className={s.cardMsg}>
                        <Message ev={ev} />
                      </div>
                    </>
                  ) : (
                    <>
                      <span className={s.time} title={formatDateTime(t)}>
                        {showDate ? `${formatDateTime(t).slice(5, 10)} ` : ''}
                        {formatClock(t)}
                      </span>
                      <span>
                        <LevelChip level={ev.level} />
                      </span>
                      <span className={s.service}>{ev.service}</span>
                      {pinned.map((p) => (
                        <span key={p} className={s.pinned}>
                          {columnValue(ev, p)}
                        </span>
                      ))}
                      <span className={s.message}>
                        <Message ev={ev} />
                      </span>
                    </>
                  )}
                </div>
                {mobile && actionsFor === ev.id && !selected ? props.renderActions(ev) : null}
                {selected ? props.renderDetail(ev) : null}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}
