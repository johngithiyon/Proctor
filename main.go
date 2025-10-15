package main

import (
    "encoding/base64"
    "encoding/json"
    "fmt"
    "html/template"
    "io/ioutil"
    "net/http"
    "net/url"
    "os"
    "path/filepath"
    "strconv"
    "strings"
    "sync"
)

var templates = template.Must(template.ParseGlob("templates/*.html"))

// --- User and Data Structures ---
var studentUser = map[string]string{
    "student1": "1234",
}
var adminUser = map[string]string{
    "admin": "admin123",
}
var exams = []string{
    "Math Exam - Grade 10",
    "Science Exam - Grade 10",
}

type Result struct {
    Username string
    Score    int
}

type Violation struct {
    Username string
    Count    int
}

type Student struct {
    Username string
}

type Question struct {
    ID      int
    Text    string
    Options []string
    Answer  string
    Time    int // Time in seconds
}

var results []Result
var violations []Violation
var students []Student
var questions []Question
var mu sync.Mutex
var questionIDCounter = 1

// Track user's current question index
var userQuestionIndex = make(map[string]int)

// Store reference faces for each user
var userReferenceFaces = make(map[string]string)

func main() {
    os.MkdirAll("captured_images", os.ModePerm)
    os.MkdirAll("reference_faces", os.ModePerm)
    os.MkdirAll("templates", os.ModePerm)

    loadExistingStudents()

    http.HandleFunc("/", loginPage)
    http.HandleFunc("/login", loginHandler)
    http.HandleFunc("/exam", examPage)
    http.HandleFunc("/proctor", proctorPage)
    http.HandleFunc("/capture", captureHandler)
    http.HandleFunc("/submit", submitHandler)
    http.HandleFunc("/score", scorePage)
    http.HandleFunc("/admin", adminPage)
    http.HandleFunc("/admin-login", ServeadminloginPage)
    http.HandleFunc("/selection", ServeselectionPage)
    http.HandleFunc("/add-question-page", Serveaddquestion) // Serves the management page
    // --- NEW/UPDATED Handlers for Question Management ---
    http.HandleFunc("/add-question", addQuestionHandler)
    http.HandleFunc("/api/questions", getQuestionsHandler)   // API to get all questions
    http.HandleFunc("/delete-question", deleteQuestionHandler) // API to delete a question
    // Other handlers
    http.HandleFunc("/add-student", addStudentHandler)
    http.HandleFunc("/delete-student", deleteStudentHandler)
    http.HandleFunc("/reference-images/", serveReferenceImage)
    http.HandleFunc("/fullscreen-violation", fullscreenViolationHandler)
    http.HandleFunc("/tab-change-violation", tabChangeViolationHandler)
    http.HandleFunc("/window-change-violation", windowChangeViolationHandler)
    http.HandleFunc("/validate-face", validateFaceHandler)
    http.HandleFunc("/get-next-question", getNextQuestionHandler)

    fmt.Println("Server running on http://localhost:8080")
    http.ListenAndServe(":8080", nil)
}

// Load existing students from reference_faces directory
func loadExistingStudents() {
    mu.Lock()
    defer mu.Unlock()

    files, err := ioutil.ReadDir("reference_faces")
    if err != nil {
        return
    }

    for _, file := range files {
        if !file.IsDir() && strings.HasSuffix(file.Name(), ".jpg") {
            username := strings.TrimSuffix(file.Name(), ".jpg")
            students = append(students, Student{Username: username})
            userReferenceFaces[username] = filepath.Join("reference_faces", file.Name())
        }
    }
}

// --- Page Renderers ---
func loginPage(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "login.html", nil)
}

func examPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    data := struct {
        Username string
        Exams    []string
    }{username, exams}
    templates.ExecuteTemplate(w, "exam.html", data)
}

func proctorPage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    exam := r.URL.Query().Get("exam")

    mu.Lock()
    userQuestionIndex[username] = 0
    mu.Unlock()

    data := struct {
        Username string
        Exam     string
    }{username, exam}

    templates.ExecuteTemplate(w, "proctor.html", data)
}

func scorePage(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    scoreStr := r.URL.Query().Get("score")
    score, _ := strconv.Atoi(scoreStr)

    data := struct {
        Username string
        Score    int
    }{username, score}
    templates.ExecuteTemplate(w, "score.html", data)
}

func adminPage(w http.ResponseWriter, r *http.Request) {
    mu.Lock()
    defer mu.Unlock()

    type AdminData struct {
        Results    []Result
        Violations []Violation
        Students   []Student
        Questions  []Question
    }

    data := AdminData{
        Results:    results,
        Violations: violations,
        Students:   students,
        Questions:  questions,
    }

    templates.ExecuteTemplate(w, "add_student.html", data)
}

// --- Handlers ---

// --- NEW: API endpoint to get all questions ---
func getQuestionsHandler(w http.ResponseWriter, r *http.Request) {
    mu.Lock()
    defer mu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(questions)
}

// --- NEW: API endpoint to delete a question ---
func deleteQuestionHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    idStr := r.FormValue("id")
    id, err := strconv.Atoi(idStr)
    if err != nil {
        http.Error(w, "Invalid question ID", http.StatusBadRequest)
        return
    }

    mu.Lock()
    defer mu.Unlock()

    for i, q := range questions {
        if q.ID == id {
            questions = append(questions[:i], questions[i+1:]...)
            w.Header().Set("Content-Type", "application/json")
            json.NewEncoder(w).Encode(map[string]string{"success": "true"})
            return
        }
    }

    http.Error(w, "Question not found", http.StatusNotFound)
}

func getNextQuestionHandler(w http.ResponseWriter, r *http.Request) {
    username := r.URL.Query().Get("user")
    if username == "" {
        http.Error(w, "User not specified", http.StatusBadRequest)
        return
    }

    mu.Lock()
    defer mu.Unlock()

    if len(questions) == 0 {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status": "no_questions"})
        return
    }

    index, ok := userQuestionIndex[username]
    if !ok {
        index = 0
        userQuestionIndex[username] = 0
    }

    if index >= len(questions) {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status": "exam_over"})
        return
    }

    currentQuestion := questions[index]
    userQuestionIndex[username]++

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(currentQuestion)
}

func addQuestionHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    questionText := r.FormValue("question")
    optionsText := r.FormValue("options")
    answer := r.FormValue("answer")
    timeStr := r.FormValue("time")

    time, err := strconv.Atoi(timeStr)
    if err != nil {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "Invalid time value"})
        return
    }

    options := strings.Split(optionsText, ",")
    for i := range options {
        options[i] = strings.TrimSpace(options[i])
    }

    mu.Lock()
    newQuestion := Question{
        ID:      questionIDCounter,
        Text:    questionText,
        Options: options,
        Answer:  answer,
        Time:    time,
    }
    questions = append(questions, newQuestion)
    questionIDCounter++
    mu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"success": "true", "message": "Question added successfully"})
}

// --- UPDATED: Redirects admin to the question page ---
func loginHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Redirect(w, r, "/", http.StatusSeeOther)
        return
    }

    username := r.FormValue("username")
    password := r.FormValue("password")
    role := r.FormValue("role")
    faceValidated := r.FormValue("face_validated")

    if role == "student" {
        if pass, ok := studentUser[username]; !ok || pass != password {
            templates.ExecuteTemplate(w, "login.html", "Invalid credentials!")
            return
        }

        mu.Lock()
        _, exists := userReferenceFaces[username]
        mu.Unlock()

        if !exists {
            templates.ExecuteTemplate(w, "login.html", "No reference image found for this student. Please contact the admin.")
            return
        }
    } else if role == "admin" {
        if pass, ok := adminUser[username]; !ok || pass != password {
            templates.ExecuteTemplate(w, "login.html", "Invalid credentials!")
            return
        }
        // --- CHANGE: Redirect admin to the question management page ---
        http.Redirect(w, r, "/add-question-page", http.StatusSeeOther)
        return
    }

    if faceValidated != "true" {
        templates.ExecuteTemplate(w, "login.html", "Face validation failed. Please try again.")
        return
    }

    if role == "student" {
        http.Redirect(w, r, "/exam?user="+username, http.StatusSeeOther)
    } else {
        templates.ExecuteTemplate(w, "login.html", "Please capture your face photo!")
    }
}

// Add student handler
func addStudentHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    username := r.FormValue("username")
    password := r.FormValue("password")
    faceImage := r.FormValue("face_image")

    mu.Lock()
    if _, exists := studentUser[username]; exists {
        mu.Unlock()
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "Username already exists"})
        return
    }

    studentUser[username] = password
    students = append(students, Student{Username: username})
    mu.Unlock()

    if faceImage == "" {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "No face image provided"})
        return
    }

    parts := strings.Split(faceImage, ",")
    if len(parts) != 2 {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "Invalid face image format"})
        return
    }

    decoded, err := base64.StdEncoding.DecodeString(parts[1])
    if err != nil {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "Error decoding face image"})
        return
    }

    referenceFacePath := filepath.Join("reference_faces", username+".jpg")
    err = ioutil.WriteFile(referenceFacePath, decoded, 0644)
    if err != nil {
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"success": "false", "message": "Error saving face image"})
        return
    }

    mu.Lock()
    userReferenceFaces[username] = referenceFacePath
    mu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"success": "true", "message": "Student added successfully"})
}

// Delete student handler
func deleteStudentHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
        return
    }

    username := r.FormValue("username")

    mu.Lock()
    defer mu.Unlock()

    delete(studentUser, username)

    if referenceFacePath, exists := userReferenceFaces[username]; exists {
        os.Remove(referenceFacePath)
        delete(userReferenceFaces, username)
    }

    for i, student := range students {
        if student.Username == username {
            students = append(students[:i], students[i+1:]...)
            break
        }
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]string{"success": "true", "message": "Student deleted successfully"})
}

// Serve reference image
func serveReferenceImage(w http.ResponseWriter, r *http.Request) {
    path := strings.TrimPrefix(r.URL.Path, "/reference-images/")
    if path == "" {
        http.NotFound(w, r)
        return
    }

    if !strings.HasSuffix(path, ".jpg") {
        path = path + ".jpg"
    }

    imagePath := filepath.Join("reference_faces", path)

    if _, err := os.Stat(imagePath); os.IsNotExist(err) {
        http.NotFound(w, r)
        return
    }

    http.ServeFile(w, r, imagePath)
}

// Validate face in the captured image
func validateFaceHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    imgData := r.FormValue("image")
    username := r.FormValue("username")

    if imgData == "" {
        w.WriteHeader(http.StatusBadRequest)
        w.Write([]byte("ERROR: No image provided"))
        return
    }

    if username != "" {
        mu.Lock()
        referenceFacePath, exists := userReferenceFaces[username]
        mu.Unlock()

        if !exists {
            w.WriteHeader(http.StatusInternalServerError)
            w.Write([]byte("ERROR: No reference face found for user"))
            return
        }

        resp, err := http.PostForm("http://localhost:5000/validate-face", url.Values{
            "image":          {imgData},
            "reference_face": {referenceFacePath},
        })
        if err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            w.Write([]byte("ERROR"))
            return
        }
        defer resp.Body.Close()

        body, _ := ioutil.ReadAll(resp.Body)
        responseStr := string(body)

        if responseStr == "FACE_MATCH" {
            w.Write([]byte("FACE_MATCH"))
        } else {
            w.Write([]byte("NO_FACE_MATCH"))
        }
    } else {
        resp, err := http.PostForm("http://localhost:5000/validate-face", url.Values{
            "image": {imgData},
        })
        if err != nil {
            w.WriteHeader(http.StatusInternalServerError)
            w.Write([]byte("ERROR"))
            return
        }
        defer resp.Body.Close()

        body, _ := ioutil.ReadAll(resp.Body)
        responseStr := string(body)

        if responseStr == "FACE_DETECTED" {
            w.Write([]byte("FACE_DETECTED"))
        } else {
            w.Write([]byte("NO_FACE_DETECTED"))
        }
    }
}

// Forward captured data to Python OpenCV service
func captureHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    imgData := r.FormValue("image")
    username := r.FormValue("username")
    noiseViolation := r.FormValue("noise_violation")

    mu.Lock()
    referenceFacePath, exists := userReferenceFaces[username]
    mu.Unlock()

    if !exists {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte("ERROR: No reference face found for user"))
        return
    }

    resp, err := http.PostForm("http://localhost:5000/capture", url.Values{
        "image":           {imgData},
        "username":        {username},
        "noise_violation": {noiseViolation},
        "reference_face":  {referenceFacePath},
    })
    if err != nil {
        w.WriteHeader(http.StatusInternalServerError)
        w.Write([]byte("ERROR"))
        return
    }
    defer resp.Body.Close()
    body, _ := ioutil.ReadAll(resp.Body)

    responseStr := string(body)

    if responseStr == "FACE_MISMATCH" {
        w.Write([]byte("FACE_MISMATCH"))
        return
    }

    if responseStr == "MULTIPLE_FACES" {
        w.Write([]byte("MULTIPLE_FACES"))
        return
    }

    if strings.HasPrefix(responseStr, "VIOLATION:") {
        respParts := strings.Split(responseStr, ":")
        if len(respParts) >= 3 {
            countStr := respParts[len(respParts)-1]
            count := 0
            fmt.Sscanf(countStr, "%d", &count)

            mu.Lock()
            found := false
            for i, v := range violations {
                if v.Username == username {
                    if count > violations[i].Count {
                        violations[i].Count = count
                    }
                    found = true

                    if violations[i].Count >= 10 {
                        mu.Unlock()
                        w.Write([]byte("MAX_VIOLATIONS"))
                        return
                    }
                    break
                }
            }

            if !found {
                violations = append(violations, Violation{Username: username, Count: count})
            }
            mu.Unlock()

            w.Write([]byte(responseStr))
            return
        }
    }

    w.Write(body)
}

// Handle fullscreen violation
func fullscreenViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")

    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true

            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }

            w.Write([]byte(fmt.Sprintf("VIOLATION:FULLSCREEN_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }

    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:FULLSCREEN_VIOLATION:1")))
    }
    mu.Unlock()
}

// Handle tab change violation
func tabChangeViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")

    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true

            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }

            w.Write([]byte(fmt.Sprintf("VIOLATION:TAB_CHANGE_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }

    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:TAB_CHANGE_VIOLATION:1")))
    }
    mu.Unlock()
}

// Handle window change violation
func windowChangeViolationHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    username := r.FormValue("username")

    mu.Lock()
    found := false
    for i, v := range violations {
        if v.Username == username {
            violations[i].Count++
            found = true

            if violations[i].Count >= 10 {
                mu.Unlock()
                w.Write([]byte("MAX_VIOLATIONS"))
                return
            }

            w.Write([]byte(fmt.Sprintf("VIOLATION:WINDOW_CHANGE_VIOLATION:%d", violations[i].Count)))
            mu.Unlock()
            return
        }
    }

    if !found {
        violations = append(violations, Violation{Username: username, Count: 1})
        w.Write([]byte(fmt.Sprintf("VIOLATION:WINDOW_CHANGE_VIOLATION:1")))
    }
    mu.Unlock()
}

func submitHandler(w http.ResponseWriter, r *http.Request) {
    if r.Method != "POST" {
        w.WriteHeader(http.StatusBadRequest)
        return
    }

    type Submission struct {
        Username string            `json:"username"`
        Answers  map[string]string `json:"answers"`
    }

    var sub Submission
    err := json.NewDecoder(r.Body).Decode(&sub)
    if err != nil {
        http.Error(w, "Error parsing request", http.StatusBadRequest)
        return
    }

    username := sub.Username
    userAnswers := sub.Answers

    mu.Lock()
    correctAnswers := make(map[string]string)
    for i, q := range questions {
        correctAnswers[strconv.Itoa(i)] = q.Answer
    }

    score := 0
    for qIndex, userAnswer := range userAnswers {
        if correctAnswer, ok := correctAnswers[qIndex]; ok && userAnswer == correctAnswer {
            score++
        }
    }

    results = append(results, Result{Username: username, Score: score})
    mu.Unlock()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "score": score})
}

func ServeadminloginPage(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "admin_login.html", nil)
}

func ServeselectionPage(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "selection.html", nil)
}

func Serveaddquestion(w http.ResponseWriter, r *http.Request) {
    templates.ExecuteTemplate(w, "add_question.html", nil)
}