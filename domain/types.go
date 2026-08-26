// Package domain holds the stable, shared domain types and value objects for
// the shield-tunnel segment quality-closure service. Every type here is
// immutable-by-convention (constructed once, never mutated in place) so that
// the append-only event log and derived snapshots can be replayed reliably.
package domain

// Section identifies a metro interval (区间), e.g. "澄江路—望塔站".
type Section string

// RingNo identifies a ring number within a section.
type RingNo int

// Generation is a monotonically increasing task/retest generation (任务代次).
type Generation int

// RingType is a catalogued ring type name (环型).
type RingType string

// WedgeDirection is the wedge orientation of a segment (楔形方向).
type WedgeDirection string

const (
	WedgeLeft  WedgeDirection = "left"
	WedgeRight WedgeDirection = "right"
	WedgeNone  WedgeDirection = "none"
)

// SegmentType classifies a segment within a ring.
type SegmentType string

const (
	SegmentKey SegmentType = "key" // 封顶块
	SegmentAdj SegmentType = "adj" // 邻接块
	SegmentStd SegmentType = "std" // 标准块
)

// JointType distinguishes longitudinal (纵缝) from circumferential (环缝) joints.
type JointType string

const (
	JointLongitudinal JointType = "longitudinal"
	JointCircum       JointType = "circumferential"
)

// ResourceKind enumerates the six single-holder leaseable resources.
type ResourceKind string

const (
	ResourceCleanBay   ResourceKind = "clean_bay"   // 清槽工位
	ResourceGlueTable  ResourceKind = "glue_table"  // 涂胶台
	ResourceRoller     ResourceKind = "roller"      // 滚压设备
	ResourceErector    ResourceKind = "erector"     // 拼装机抓取位
	ResourceTorqueTool ResourceKind = "torque_tool" // 扭矩工具
	ResourceLeakBay    ResourceKind = "leak_bay"    // 检漏分舱
)

// RuleSummary is a content-addressed digest that freezes a rule catalog
// version so that a locked task can prove its rules were fresh at lock time.
type RuleSummary string

// RuleSnapshot freezes the full rule catalogue relevant to a locked task.
type RuleSnapshot struct {
	Summary         RuleSummary       `json:"summary"`
	Section         Section           `json:"section"`
	RingType        RingType          `json:"ring_type"`
	CenterAngleSum  int64             `json:"center_angle_sum"` // integer degrees (非负整数)
	SegmentTemplate []SegmentTemplate `json:"segment_template"`
	WedgeConstraint WedgeDirection    `json:"wedge_constraint"`
	GrooveGeometry  GrooveGeometry    `json:"groove_geometry"`
	HoleGeometry    HoleGeometry      `json:"hole_geometry"`
	JointTemplates  []JointTemplate   `json:"joint_templates"`
	MaterialSpec    MaterialSpec      `json:"material_spec"`
	ProcessSpec     ProcessSpec       `json:"process_spec"`
	LeakBayGraph    [][]int           `json:"leak_bay_graph"` // 检漏分舱邻接表
	Thresholds      Thresholds        `json:"thresholds"`
}

// SegmentTemplate describes the catalogued geometry for one segment class.
type SegmentTemplate struct {
	Type        SegmentType `json:"type"`
	CenterAngle int64       `json:"center_angle"` // integer degrees
}

// GrooveGeometry is the integer-millimetre seal-groove definition.
type GrooveGeometry struct {
	WidthMM    int64 `json:"width_mm"`
	DepthMM    int64 `json:"depth_mm"`
	CornerMM   int64 `json:"corner_mm"`
	JointPosMM int64 `json:"joint_pos_mm"`
}

// HoleGeometry is the integer-millimetre bolt-hole definition.
type HoleGeometry struct {
	Count     int   `json:"count"`
	SpacingMM int64 `json:"spacing_mm"`
}

// JointTemplate describes pairing rules for a joint class.
type JointTemplate struct {
	Type        JointType `json:"type"`
	PairingRule string    `json:"pairing_rule"`
}

// MaterialSpec holds catalogued material requirements.
type MaterialSpec struct {
	GasketBatch   string `json:"gasket_batch"`
	AdhesiveBatch string `json:"adhesive_batch"`
}

// ProcessSpec orders the paste/assembly/tightening dependency chain.
type ProcessSpec struct {
	PasteStart    int   `json:"paste_start"` // 粘贴起点分段序号
	AssemblyOrder []int `json:"assembly_order"`
	BoltStages    []int `json:"bolt_stages"` // 紧固级次
}

// Thresholds holds the integer fixed-point acceptance thresholds.
type Thresholds struct {
	ElongationMax    Fixed `json:"elongation_max"`
	LapRateMin       Fixed `json:"lap_rate_min"`
	GluePerLengthMax Fixed `json:"glue_per_length_max"`
	OpenTimeMax      int64 `json:"open_time_max"` // 逻辑时间上限（开放时限）
	OpeningMax       Fixed `json:"opening_max"`
	OffsetMax        Fixed `json:"offset_max"`
	CompressionMin   Fixed `json:"compression_min"`
	PreloadDevMax    Fixed `json:"preload_dev_max"`
	DecayRateMax     Fixed `json:"decay_rate_max"`
	RetryLimit       int   `json:"retry_limit"`
}

// RingTask is the aggregate root for one locked ring task (整环任务).
type RingTask struct {
	ID           string        `json:"id"` // opaque stable id
	Section      Section       `json:"section"`
	RingNo       RingNo        `json:"ring_no"`
	Generation   Generation    `json:"generation"`
	Rule         RuleSnapshot  `json:"rule"`
	Segments     []Segment     `json:"segments"`
	Joints       []Joint       `json:"joints"`
	SealSections []SealSection `json:"seal_sections"`
	LockedAt     int64         `json:"locked_at"` // logical time of lock
}

// Segment is a positioned, uniquely-identified ring segment (管片).
type Segment struct {
	Seq         int            `json:"seq"`
	Type        SegmentType    `json:"type"`
	CenterAngle int64          `json:"center_angle"`
	Wedge       WedgeDirection `json:"wedge"`
	Groove      GrooveGeometry `json:"groove"`
	Holes       HoleGeometry   `json:"holes"`
}

// Joint is one longitudinal/circumferential joint pairing (接缝).
type Joint struct {
	Type  JointType   `json:"type"`
	EdgeA SegmentEdge `json:"edge_a"`
	EdgeB SegmentEdge `json:"edge_b"`
}

// SegmentEdge uniquely names one side of a segment for pairing.
type SegmentEdge struct {
	SegmentSeq int    `json:"segment_seq"`
	Side       string `json:"side"`
}

// SealSection is one ordered sealing subsection (密封分段).
type SealSection struct {
	Seq        int    `json:"seq"`
	SegmentSeq int    `json:"segment_seq"`
	Joint      *Joint `json:"joint,omitempty"` // nil when interior to a single segment
	Status     string `json:"status"`          // append-only state label
}

// GasketBar is a uniquely-identified raw gasket bar (密封垫原条).
type GasketBar struct {
	ID            string       `json:"id"`
	Batch         string       `json:"batch"`
	TotalLengthMM int64        `json:"total_length_mm"`
	BoundToSlot   *SegmentSlot `json:"bound_to_slot,omitempty"` // 单槽绑定, nil if unbound
}

// SegmentSlot names the slot to which a gasket bar is bound.
type SegmentSlot struct {
	SegmentSeq int `json:"segment_seq"`
}

// GasketAllocation records one integer-millimetre allocation of a bar.
type GasketAllocation struct {
	BarID    string `json:"bar_id"`
	Kind     string `json:"kind"` // valid|lap|sample|remainder|loss
	LengthMM int64  `json:"length_mm"`
}

// AdhesiveIssue records one integer-milligram adhesive issue (胶粘剂领用).
// Conservation requires TotalMg == AppliedMg+RetainedMg+RecoveredMg+LossMg,
// with every component non-negative. It is immutable once committed.
type AdhesiveIssue struct {
	Batch       string     `json:"batch"`
	Generation  Generation `json:"generation"`
	TotalMg     int64      `json:"total_mg"`
	AppliedMg   int64      `json:"applied_mg"`
	RetainedMg  int64      `json:"retained_mg"`
	RecoveredMg int64      `json:"recovered_mg"`
	LossMg      int64      `json:"loss_mg"`
}

// MaterialLedgerEntry is an immutable, append-only ledger row.
type MaterialLedgerEntry struct {
	Seq       int64  `json:"seq"`
	Kind      string `json:"kind"`     // gasket | adhesive
	DeltaMM   int64  `json:"delta_mm"` // for gasket rows
	DeltaMg   int64  `json:"delta_mg"` // for adhesive rows
	Operation string `json:"operation"`
}

// ResourceLease is a single-holder, time-bounded lease (资源租约).
type ResourceLease struct {
	Resource   ResourceKind `json:"resource"`
	ResourceID string       `json:"resource_id"`
	Holder     string       `json:"holder"`
	Start      int64        `json:"start"` // inclusive logical time
	End        int64        `json:"end"`   // exclusive logical time
}

// OperationReceipt records an idempotent operation result.
type OperationReceipt struct {
	OperationID string     `json:"operation_id"`
	ContentHash string     `json:"content_hash"`
	Result      string     `json:"result"`
	Generation  Generation `json:"generation,omitempty"`
}

// DeviceAttempt is an append-only scripted device call record.
type DeviceAttempt struct {
	DeviceType  string `json:"device_type"`
	CallNo      int    `json:"call_no"`
	LogicalTime int64  `json:"logical_time"`
	RetrySeq    int    `json:"retry_seq"`
	FaultCode   string `json:"fault_code"`
	Reading     *Fixed `json:"reading,omitempty"` // nil unless a valid measurement was produced
}

// ProcessEvidence is a dependency-positioned process record.
type ProcessEvidence struct {
	Kind         string     `json:"kind"` // clean|dry|cut|joint|glue|paste|roll|cure|seat
	Generation   Generation `json:"generation"`
	LogicalTime  int64      `json:"logical_time"`
	PrefixLen    int        `json:"prefix_len"`
	InstrumentID string     `json:"instrument_id"`
}

// BoltStageEvidence records one bolt preload stage.
type BoltStageEvidence struct {
	Stage       int        `json:"stage"`
	Generation  Generation `json:"generation"`
	LogicalTime int64      `json:"logical_time"`
	PreloadDev  Fixed      `json:"preload_dev"`
}

// GeometryEvidence records a seam geometry measurement.
type GeometryEvidence struct {
	Kind         string     `json:"kind"` // opening|offset|compression
	Joint        Joint      `json:"joint"`
	Generation   Generation `json:"generation"`
	LogicalTime  int64      `json:"logical_time"`
	Value        Fixed      `json:"value"`
	InstrumentID string     `json:"instrument_id"`
}

// PressureTrace is one compartment pressure reading on the time axis.
type PressureTrace struct {
	Bay         int   `json:"bay"`
	LogicalTime int64 `json:"logical_time"`
	Pressure    Fixed `json:"pressure"`
}

// RetestCase links an anomaly source to its deterministic propagation set.
type RetestCase struct {
	ID         string     `json:"id"`
	Source     string     `json:"source"`
	Affected   []int      `json:"affected"` // sorted, de-duplicated joint/segment ids
	Generation Generation `json:"generation"`
	Resolved   bool       `json:"resolved"` // true once re-verification has closed the retest
}

// Review is one independent qualified reviewer sign-off.
type Review struct {
	Reviewer   string     `json:"reviewer"`
	Qualified  bool       `json:"qualified"`
	Generation Generation `json:"generation"`
	Approved   bool       `json:"approved"`
}

// TerminalDecision is the single irreversible terminal verdict.
type TerminalDecision struct {
	Kind       string     `json:"kind"` // admit | isolate | cancel
	Generation Generation `json:"generation"`
	Credential string     `json:"credential"` // 准入凭据摘要
}
