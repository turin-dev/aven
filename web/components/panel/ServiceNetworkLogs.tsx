"use client";

import { useEffect, useMemo, useState } from "react";
import { sse, type ID, type NetworkCaptureStatus } from "@/lib/api";
import { useLogStream, useParsedLines } from "@/lib/useLogStream";
import { NetworkLogTable, type NetworkLogRecord } from "../NetworkLogTable";
import { t } from "@/lib/i18n";

/** DNS query or network flow logs of one service (deployment panel › 네트워크 로그). The filter runs on the server. */
export function ServiceNetworkLogs({ sid, eid, kind, filter }: { sid: ID; eid: ID; kind: "dns" | "network"; filter: string }) {
  const [query, setQuery] = useState(filter);
  useEffect(() => {
    const timer = setTimeout(() => setQuery(filter), 400);
    return () => clearTimeout(timer);
  }, [filter]);
  const [capture, setCapture] = useState<NetworkCaptureStatus | null>(null);
  const on = useMemo(
    () => ({
      capture: (data: string) => {
        try {
          setCapture(JSON.parse(data) as NetworkCaptureStatus);
        } catch {
          setCapture(null);
        }
      },
    }),
    [],
  );
  const stream = useLogStream(sse.serviceNetworkLogs(sid, eid, kind, query), { replay: true, maxLines: 5000, on });
  const records = useParsedLines<NetworkLogRecord>(stream.lines);
  return (
    <NetworkLogTable
      className="h-full"
      kind={kind}
      records={records}
      state={stream.state}
      capture={capture}
      showService={false}
      emptyText={
        query.trim()
          ? t("필터와 일치하는 기록이 없습니다.")
          : kind === "dns"
            ? t("아직 기록된 DNS 조회가 없습니다. 같은 환경의 서비스 이름(alias)은 컨테이너 안에서 바로 풀려 기록되지 않고, 외부 이름과 찾지 못한 이름이 기록됩니다.")
            : t("아직 기록된 연결이 없습니다. 이 서비스가 주고받는 TCP/UDP 연결이 여기에 표시됩니다.")
      }
    />
  );
}
