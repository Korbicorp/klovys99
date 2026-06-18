package anonymizer

type EntityType string

const (
	EntityEmail             EntityType = "EMAIL"
	EntityIP                EntityType = "IP"
	EntityPhone             EntityType = "PHONE"
	EntityNIR               EntityType = "NIR"
	EntityFirstName         EntityType = "FIRST_NAME"
	EntityLastName          EntityType = "LAST_NAME"
	EntityAddress           EntityType = "ADDRESS"
	EntityIBAN              EntityType = "IBAN"
	EntityCreditCard        EntityType = "CREDIT_CARD"
	EntityMACAddress        EntityType = "MAC_ADDRESS"
	EntityCrypto            EntityType = "CRYPTO"
	EntityURL               EntityType = "URL"
	EntitySecret            EntityType = "SECRET"
	EntityNumericID         EntityType = "NUMERIC_ID"
	EntityReferenceID       EntityType = "REFERENCE_ID"
	EntityName              EntityType = "NAME"
	EntityPersonName        EntityType = "PERSON_NAME"
	EntityLocation          EntityType = "LOCATION"
	EntityOrganization      EntityType = "ORGANIZATION"
	EntityContextIdentifier EntityType = "CONTEXT_IDENTIFIER"
	EntityOtherPII          EntityType = "OTHER_PII"
	EntityDate              EntityType = "DATE"
	EntityBirthDate         EntityType = "BIRTH_DATE"
	EntityBloodType         EntityType = "BLOOD_TYPE"
	EntityDocumentID        EntityType = "DOCUMENT_ID"
	EntityVehiclePlate      EntityType = "VEHICLE_PLATE"
	EntityMedicalProvider   EntityType = "MEDICAL_PROVIDER"
	EntitySchool            EntityType = "SCHOOL"
	EntityEmployer          EntityType = "EMPLOYER"
	EntityPetIdentifier     EntityType = "PET_IDENTIFIER"
)

type Match struct {
	// Start is the byte offset where the sensitive value starts in the input.
	Start int
	// End is the byte offset just after the sensitive value in the input.
	End int
	// Type controls the token prefix used for replacement, for example EMAIL.
	Type EntityType
	// Priority resolves overlapping matches; higher priority wins.
	Priority int
	// Normalized is the canonical value used to keep pseudonyms stable.
	Normalized string
}

func (m Match) Len() int {
	return m.End - m.Start
}

type Detector interface {
	// FindAll returns every sensitive span detected in text.
	FindAll(text []byte) []Match
}

type EntityStats struct {
	// Count is the number of replacements made for this entity type.
	Count int
}

type Result struct {
	// Stats is keyed by entity type and summarizes replacements for one call.
	Stats map[EntityType]EntityStats
}
