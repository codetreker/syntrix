package syntrix

// Synctrix service interface
type Service interface {
}

func NewService() Service {
	return &syntrixService{}
}

type syntrixService struct {
}
