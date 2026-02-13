package xmlstream

import (
	"bytes"
	"fmt"
)

func ExampleScanner() {
	data := []byte(`
		<Person>
			<FullName>Jon Arbuckle</FullName>
			<Email where="home">
				<Addr>jon@example.com</Addr>
			</Email>
			<Email where='work'>
				<Addr>jon@work.com</Addr>
			</Email>
		</Person>

		<Cat>
			<Nickname>Garfield</Nickname>
			<Breed>Red British Shorthair</Breed>
		</Cat>

		<Person>
			<FullName>Dr. Liz Wilson</FullName>
			<Email where="home">
				<Addr>liz@example.com</Addr>
			</Email>
			<Email where='work'>
				<Addr>liz@work.com</Addr>
			</Email>
		</Person>
	`)

	// Define custom structs to hold data.
	type Email struct {
		Where string `xml:"where,attr"`
		Addr  string
	}
	type Person struct {
		Name   string  `xml:"FullName"`
		Emails []Email `xml:"Email"`
	}
	type Cat struct {
		Name  string `xml:"Nickname"`
		Breed string
	}

	// Create a scanner.
	scanner := NewScanner(bytes.NewReader(data), new(Person), new(Cat))

	// Iterate through it.
	personCounter := 0
	catCounter := 0
	for scanner.Scan() {
		tag := scanner.Element()
		switch el := tag.(type) {
		case *Person:
			person := *el
			personCounter++
			fmt.Printf("Human N°%d: %s, %s\n", personCounter, person.Name, person.Emails)

		case *Cat:
			cat := *el
			catCounter++
			fmt.Printf("Cat N°%d: %s, %s\n", catCounter, cat.Name, cat.Breed)
		}
	}

	// Check errors.
	if err := scanner.Err(); err != nil {
		fmt.Printf("Error while scanning XML: %v\n", err)
	}

	// Output:
	// Human N°1: Jon Arbuckle, [{home jon@example.com} {work jon@work.com}]
	// Cat N°1: Garfield, Red British Shorthair
	// Human N°2: Dr. Liz Wilson, [{home liz@example.com} {work liz@work.com}]
}
