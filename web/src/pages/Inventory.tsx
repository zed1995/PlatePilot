import { useCallback, useEffect, useState } from 'react'
import { Card, CardContent } from '../components/ui/card'
import { Input } from '../components/ui/input'
import { Button } from '../components/ui/button'
import { PageHeader } from '../components/page-header'
import { ErrorState } from '../components/error-state'
import { adminApi } from '../api/client'
import type { InventoryView, ReservationRecord, ReservationSlot } from '../api/types'

// The mock inventory page exists for one reason: a demo must be replayable.
// An operator looks at what a demo run spent (slots with booked counts and
// the reservations behind them) and resets it back to pristine before the
// next run. The reset is the one write this read-only console performs, and
// it is deliberately behind a confirm.
export function Inventory() {
  const [restaurantId, setRestaurantId] = useState('')
  const [date, setDate] = useState('')
  const [view, setView] = useState<InventoryView | null>(null)
  const [error, setError] = useState<Error | null>(null)
  const [loading, setLoading] = useState(false)
  const [resetting, setResetting] = useState(false)
  const [resetNote, setResetNote] = useState('')

  const load = useCallback(async (id: string, d: string) => {
    const numeric = Number(id)
    if (!id || Number.isNaN(numeric)) return
    setLoading(true)
    setError(null)
    try {
      const data = await adminApi.inventory(numeric, d || undefined)
      setView(data)
    } catch (e) {
      setError(e as Error)
      setView(null)
    } finally {
      setLoading(false)
    }
  }, [])

  // Auto-load once a syntactically valid id is entered.
  useEffect(() => {
    const numeric = Number(restaurantId)
    if (restaurantId && !Number.isNaN(numeric)) void load(restaurantId, date)
  }, [restaurantId, date, load])

  const onReset = async () => {
    const numeric = Number(restaurantId)
    if (!restaurantId || Number.isNaN(numeric)) return
    if (!window.confirm('重置将删除该餐厅的全部 Mock 预约并清空已订座位，确定继续？')) return
    setResetting(true)
    setError(null)
    try {
      const result = await adminApi.resetInventory(numeric, date || undefined)
      setResetNote(`已重置 ${result.slots_reset} 个时段，删除 ${result.reservations_removed} 条预约`)
      await load(restaurantId, date)
    } catch (e) {
      setError(e as Error)
    } finally {
      setResetting(false)
    }
  }

  const columns = 'grid grid-cols-[1fr_1fr_1fr_1fr_1fr] gap-2'

  return (
    <div className="space-y-5">
      <PageHeader
        title="Mock 库存"
        description="查看与重置预约演示用的 Mock 库存。重置仅影响演示数据。"
      />
      <Card>
        <CardContent className="pt-5">
          <div className="flex flex-wrap items-end gap-3">
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">Restaurant ID</label>
              <Input
                className="w-[150px]"
                placeholder="例如 42"
                value={restaurantId}
                onChange={(e) => setRestaurantId(e.target.value.replace(/[^0-9]/g, ''))}
              />
            </div>
            <div className="flex flex-col gap-1">
              <label className="text-[10px] uppercase tracking-[0.04em] text-ink-tertiary">日期（可选）</label>
              <Input
                className="w-[170px]"
                type="date"
                value={date}
                onChange={(e) => setDate(e.target.value)}
              />
            </div>
            <Button
              variant="ghost"
              size="sm"
              onClick={() => { setDate(''); setResetNote('') }}
            >
              清除日期
            </Button>
            <div className="ml-auto flex items-center gap-3">
              {resetNote && <span className="text-[12px] text-ink-secondary">{resetNote}</span>}
              <Button size="sm" onClick={onReset} disabled={!restaurantId || resetting}>
                {resetting ? '重置中…' : '重置库存'}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>

      {error && (
        <ErrorState
          title="加载库存失败"
          description={error.message}
          onRetry={() => void load(restaurantId, date)}
        />
      )}
      {loading && <p className="text-[13px] text-ink-secondary">加载中…</p>}

      {view && !loading && (
        <>
          <Card>
            <CardContent className="pt-5">
              <div className="mb-2 flex items-baseline justify-between">
                <h2 className="text-[14px] font-medium">时段（{view.slots.length}）</h2>
                {view.date && <span className="text-[12px] text-ink-tertiary">{view.date}</span>}
              </div>
              {view.slots.length === 0 ? (
                <p className="text-[13px] text-ink-tertiary">该范围没有任何时段。</p>
              ) : (
                <div className="space-y-1">
                  <div className={`${columns} text-[10px] uppercase tracking-[0.04em] text-ink-tertiary`}>
                    <span>时段</span><span>日期</span><span>容量</span><span>已订</span><span>剩余</span>
                  </div>
                  {view.slots.map((s: ReservationSlot) => (
                    <div key={s.slot_id} className={`${columns} text-[13px] tabular`}>
                      <span>{s.slot_time}</span>
                      <span>{s.slot_date}</span>
                      <span>{s.capacity}</span>
                      <span>{s.booked}</span>
                      <span>{Math.max(0, s.capacity - s.booked)}</span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
          <Card>
            <CardContent className="pt-5">
              <h2 className="mb-2 text-[14px] font-medium">预约（{view.reservations.length}）</h2>
              {view.reservations.length === 0 ? (
                <p className="text-[13px] text-ink-tertiary">该范围没有任何预约。</p>
              ) : (
                <div className="space-y-1">
                  {view.reservations.map((r: ReservationRecord) => (
                    <div key={r.reservation_id} className="flex items-center gap-3 text-[13px]">
                      <span className="font-medium">{r.reservation_id}</span>
                      <span className="text-ink-secondary">{r.slot_id}</span>
                      <span>{r.party_size} 人</span>
                      <span className="text-ink-secondary">{r.status}</span>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </>
      )}
    </div>
  )
}

export default Inventory
