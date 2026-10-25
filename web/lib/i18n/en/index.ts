// 그룹별 사전을 합친다. 같은 키가 여러 그룹에 있으면 뒤의 것이 이긴다(같은 원문은 같은 번역이어야 한다).
import g01 from "./g01";
import g02 from "./g02";
import g03 from "./g03";
import g04 from "./g04";
import g05 from "./g05";
import g06 from "./g06";
import g07 from "./g07";
import g08 from "./g08";
import g09 from "./g09";
import g10 from "./g10";
import g11 from "./g11";
import g12 from "./g12";
import g13 from "./g13";
import g14 from "./g14";
import common from "./common";

export const EN: Record<string, string> = { ...common, ...g01, ...g02, ...g03, ...g04, ...g05, ...g06, ...g07, ...g08, ...g09, ...g10, ...g11, ...g12, ...g13, ...g14 };
