# SQLite 線上遷移至 MySQL

[简体中文](../../operations/mysql-migration) | [English](../../en/operations/mysql-migration) | [繁體中文](./mysql-migration) | [Русский](../../ru/operations/mysql-migration)

Manager Server 可在保留 SQLite 預設行為的同時，將完整歷史資料遷移至外部 MySQL。切換後，SQLite 仍作為設定資料庫與近期資料快取。此功能適用於單一 Manager Server 程序；不會部署內建 MySQL 容器，也不會自動進行容錯移轉。

## 支援的目標

- 支援官方 MySQL 8.x，最低版本為 8.0.12；不支援 MariaDB 或 MySQL 9.x。
- MySQL 8.0.17+ schema 使用 `utf8mb4_0900_bin`；8.0.12–8.0.16 會自動使用該版本可用、NO PAD、區分大小寫與重音的 `utf8mb4_0900_as_cs`。工作階段統一使用 UTC、嚴格 SQL 模式及 `innodb_strict_mode=ON`。
- 帳號必須在選定 schema 上具有 `SELECT`、`INSERT`、`UPDATE`、`DELETE`、`CREATE`、`ALTER`、`DROP`、`INDEX`、`REFERENCES` 與 `TRIGGER` 權限。CPAMP 會使用隨機探測表驗證這些權限，而非僅信任對 `SHOW GRANTS` 的解析結果。
- 若啟用 binary logging，且一般 schema 帳號在建立 trigger 時收到 Error 1419，請要求 DBA 設定並持久化 `log_bin_trust_function_creators=ON`，或由具備所需管理權限的 DBA 安裝 trigger；請勿將 `SUPER` 授予應用程式帳號。
- 外部 MySQL 預設使用憑證身分驗證。停用 TLS 時會顯示安全警告，並需要再次確認。
- `max_allowed_packet` 至少必須為 4 MiB。若任一權威 SQLite 資料列無法放入單一 MySQL 協定封包，預檢會停止。

## 資料路由

在正常模式下，每次權威寫入會提交至 SQLite，並在同一筆交易中附加完整的 Outbox 群組。工作程序最終會將其套用至 MySQL。讀取切換後，業務查詢會優先使用 MySQL，僅在已分類的連線或可用性故障時回退至 SQLite。SQL、schema、權限、掃描及資料錯誤絕不會被回退機制掩蓋。

SQLite 會永久保留設定、管理員憑證、資料庫連線設定、模型價格及其 context/service tier，以及 API Key 別名。切換並完成受防護的清理後，其他業務資料預設僅作為 15 天快取保留，而 MySQL 保留完整歷史。進行中、待處理或未完成的動作、冷卻、巡檢與配額生命週期紀錄不會因資料年限而移除。

## 開始前

1. 備份完整的 CPAMP 資料目錄，包括 SQLite/WAL/SHM、`database-control.json.enc`、其 `.bak` 及 `data.key`。
2. 設定獨立的 MySQL 備份，並完成一次還原演練。
3. 確認目標資料庫為空，或與 CPAMP 顯示的 schema manifest 完全相容。
4. 確認完整歷史資料所需的容量、`max_allowed_packet`、連線限制與備份保留期。
5. 請在離峰期間操作。最終驗證會短暫暫停 Collector 與背景寫入工作程序。

## 三個遷移階段

### 1. 測試 MySQL 並啟用雙寫

開啟 **系統 → 資料庫拓撲**，輸入位址、資料庫、帳號、密碼、TLS 模式與 CA 憑證，然後選取 **測試 MySQL**。儲存連線後，選取 **啟用雙寫**。

後端會驗證版本、字元集/collation、UTC、嚴格模式、封包大小與實際 DDL/DML 存取權。它會建立完整 schema、複製每個設定欄位、啟用 SQLite Outbox，並開始即時同步。此時業務讀取仍使用 SQLite。

密碼絕不會傳回瀏覽器或寫入日誌。之後儲存時將其留空，會保留現有密碼。

若升級後 MySQL 資料表結構已過期，請先停用同步與遷移，並確認 MySQL 未承擔讀寫，再選取 **重新初始化 MySQL 資料表結構**。頁面會連續顯示兩次危險確認，並列出目前資料庫名稱；後端還會核對目標、資料庫名稱、routing generation 與冪等鍵。確認後，CPAMP 會刪除目前已設定資料庫中的所有資料表、檢視、觸發器及資料，再依最新 schema manifest 重建。它不會刪除資料庫本身、不影響 SQLite，也不會自動複製資料、啟用同步或變更路由。使用前必須完成 MySQL 備份。

### 2. 遷移歷史資料

選取 **遷移歷史資料**。CPAMP 會依外鍵順序，以穩定的主鍵/rowid 水位複製每張權威表，保留明確 ID、NULL、原始 JSON、失敗本文、用戶端/IP 資料、token、service tier、價格、巡檢、動作、冷卻及配額生命週期欄位。

預設批次為 1,000 列，並受 4 MiB 讀取預算限制；超大列會獨佔一個批次。持久 checkpoint 支援暫停、繼續、重試與程序重新啟動。歷史批次無法覆寫已由 Outbox 傳送的較新列。接著 MySQL 會從權威列重建彙總、projection、索引與搜尋資料。

系統資訊中的 **遷移任務記錄** 會列出持久化任務、目前階段與狀態、逐表列數/位元組/請求數，以及完整執行時間線。背景錯誤會原樣寫入追加式稽核記錄；恢復任務只會清除目前告警，不會刪除先前的失敗原因。管理員亦可透過 `GET /v0/management/databases/migrations?limit=20` 查詢相同記錄。

### 3. 驗證並切換業務讀取

等待積壓量為 `0`，然後選取 **驗證**。CPAMP 會短暫取得全域寫入柵欄，讓 MySQL 追上最終 Outbox 水位，並比較 schema、列數、主鍵範圍、外鍵、欄位正規化後的 SHA-256、token、成功/失敗數量，以及依凍結價格簿快照計算的費用。

僅在每項檢查和衍生/搜尋一致性合約皆通過後，才會提供 validation token 與 **切換業務主讀**。切換需要目前的 migration ID、token、目標與 routing generation；過期值會傳回 `409`。切換後，SQLite 仍是預設寫入前置層與近期快取。

## 快取清理

定時清理預設關閉（頁面中的核取方塊預設不勾選），啟動或升級不會自動刪除 SQLite 資料。管理員明確啟用後，在 MySQL 讀取切換前仍不能清理 SQLite 歷史資料；當切換、驗證與同步水位均安全後，先預覽清理，再啟動有界的清理工作。MySQL 不可用、Outbox 尚有待處理資料、遷移未完成或驗證過期時，該工作會自動暫停。既有安裝會保留先前儲存的開關狀態。

手動將寫入主庫容錯移轉、清理 SQLite 及重建 SQLite 快取皆屬危險操作。除了目前的 routing generation 與冪等鍵外，每個請求還必須回傳 UI 目前顯示的目標後端、migration ID 及 validation token。後端會再根據加密控制檔與持久化遷移狀態檢查它們；缺少或過期的確認絕不會執行。

線上刪除會讓 SQLite 頁面可重複使用；不會立即縮小實體 `usage.sqlite` 檔案。系統資訊會分別報告實體大小、有效頁面與可重複使用空間。Manager Server 運作時，請勿執行外部 `VACUUM` 或替換資料庫檔案。

## 故障與復原

- **MySQL 中斷：**SQLite 繼續接受寫入，並累積 Outbox。回退回應會包含 `X-CPAMP-Data-Source`、完整性與快取覆蓋範圍 headers，UI 會持續顯示部分資料警告。
- **SQLite 無法寫入：**CPAMP 會進入唯讀/待切換狀態並發出警報。它絕不會自動提升 MySQL；管理員必須在確認容錯移轉前驗證目標、epoch 與水位。
- **從 MySQL 重建 SQLite：**此操作會還原所有設定，且僅還原最近 N 天的業務快取。它會在短暫寫入柵欄下進行原子替換前，先建立並驗證暫存 SQLite 資料庫。這不是將完整歷史遷回 SQLite。
- **MySQL 寫入後的 SQLite 復原：**切換回來前，請等待反向 Outbox 追平並完成驗證。來自舊 fencing epoch 的寫入會被拒絕。

若積壓存在且 60 秒沒有進度，請檢查連線、磁碟、鎖定與權限。CPAMP 會在 120 秒後將工作程序標示為 `stalled`。絕不可啟動第二個 Manager Server 程序來「加速」遷移。
