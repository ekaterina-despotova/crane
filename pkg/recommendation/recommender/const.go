package recommender

const (
	// ReplicasRecommender name
	ReplicasRecommender string = "Replicas"

	// ResourceRecommender name
	ResourceRecommender string = "Resource"

	// HPARecommender name
	HPARecommender string = "HPA"

	// IdleNodeRecommender name
	IdleNodeRecommender string = "IdleNode"

	// VolumeRecommender name
	VolumeRecommender string = "Volume"

	// ServiceRecommender name
	ServiceRecommender string = "Service"

	// CarbonIdleResourceRecommender name
	CarbonIdleResourceRecommender string = "CarbonIdleResource"

	// CarbonRightSizingRecommender name (vertical: container resource requests/limits)
	CarbonRightSizingRecommender string = "CarbonRightSizing"

	// CarbonHorizontalScalingRecommender name (horizontal: replica count)
	CarbonHorizontalScalingRecommender string = "CarbonHorizontalScaling"

	// CarbonTemporalShiftingRecommender name
	CarbonTemporalShiftingRecommender string = "CarbonTemporalShifting"

	// CarbonSpatialShiftingRecommender name
	CarbonSpatialShiftingRecommender string = "CarbonSpatialShifting"
)
