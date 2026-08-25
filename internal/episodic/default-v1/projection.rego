package memories.attributes

default attributes = {}

attributes = {"namespace": input.namespace[0], "sub": input.namespace[1]} if {
    count(input.namespace) >= 2
}
